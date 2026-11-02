// Command imagetool supports images/build.sh: it prepares the staging tree,
// computes the content manifest, emits the debugfs ownership script, and
// verifies the finished image against images/layout.json.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"agentevalharness/internal/sandbox"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fail("usage: imagetool prepare|manifest|script|verify [flags]")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	root := fs.String("root", "", "staging tree")
	layoutPath := fs.String("layout", "images/layout.json", "layout file")
	image := fs.String("image", "", "built ext4 image")
	out := fs.String("out", "", "manifest output path")
	entriesOut := fs.String("entries", "", "optional per-entry listing output (jsonl)")
	var meta multi
	fs.Var(&meta, "meta", "key=value recorded in the manifest (repeatable)")
	_ = fs.Parse(args)

	layout, layoutRaw, err := sandbox.LoadLayout(*layoutPath)
	if err != nil {
		fail("layout: %v", err)
	}
	switch cmd {
	case "prepare":
		removed, err := sandbox.Prepare(need(*root, "--root"), layout)
		if err != nil {
			fail("prepare: %v", err)
		}
		fmt.Printf("removed %d path(s): %s\n", len(removed), strings.Join(removed, " "))
	case "script":
		entries := walk(*root, layout)
		s, err := sandbox.DebugfsScript(entries)
		if err != nil {
			fail("script: %v", err)
		}
		fmt.Print(s)
	case "manifest":
		entries := walk(*root, layout)
		m := map[string]any{
			"schema_version": 1,
			"tree_hash":      sandbox.TreeHash(entries),
			"entry_count":    len(entries),
			"layout_sha256":  sha(layoutRaw),
			"meta":           parseMeta(meta),
		}
		if *image != "" {
			h, err := sandbox.Sha256File(*image)
			if err != nil {
				fail("image hash: %v", err)
			}
			m["image_sha256_informational"] = h // ext4 images are not bit-reproducible
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		if *out == "" {
			fmt.Println(string(b))
		} else if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			fail("write manifest: %v", err)
		}
		if *entriesOut != "" {
			writeEntries(*entriesOut, entries)
		}
		fmt.Fprintf(os.Stderr, "tree_hash %s (%d entries)\n", m["tree_hash"], len(entries))
	case "verify":
		bad := sandbox.VerifyImage(context.Background(), sandbox.NewSystem(), need(*image, "--image"), layout.Invariants)
		if len(bad) > 0 {
			for _, b := range bad {
				fmt.Fprintln(os.Stderr, "FAIL:", b)
			}
			os.Exit(1)
		}
		fmt.Printf("image satisfies all %d layout invariants\n", len(layout.Invariants))
	default:
		fail("unknown subcommand %q", cmd)
	}
}

func walk(root string, l *sandbox.Layout) []sandbox.Entry {
	entries, err := sandbox.Walk(need(root, "--root"), l)
	if err != nil {
		fail("walk: %v", err)
	}
	return entries
}

func writeEntries(path string, entries []sandbox.Entry) {
	f, err := os.Create(path)
	if err != nil {
		fail("entries: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		_ = enc.Encode(map[string]any{"path": e.Path, "kind": string(e.Kind), "perm": fmt.Sprintf("%04o", e.Perm), "uid": e.UID, "gid": e.GID, "sha256": e.SHA256, "link": e.Link})
	}
}

func parseMeta(m multi) map[string]string {
	out := map[string]string{}
	for _, kv := range m {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fail("--meta wants key=value, got %q", kv)
		}
		out[k] = v
	}
	return out
}

func sha(b []byte) string { return sandboxSum(b) }

func need(v, flagName string) string {
	if v == "" {
		fail("%s is required", flagName)
	}
	return v
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
