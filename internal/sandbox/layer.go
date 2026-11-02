package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildLayer packs the tree at srcRoot into a read-only ext4 layer that the
// guest stacks between the base image and the workspace. Every object, including
// the top-level directories that show through the overlay, is owned by root, so
// the agent can read and run the toolchain but not change it in place. Paths in
// srcRoot are guest absolute paths (srcRoot/opt/env is /opt/env). It returns the
// layer's entries, which the diff needs to tell a create from a modify.
func BuildLayer(ctx context.Context, sys System, srcRoot, outImage, label string) ([]Entry, error) {
	root := 0
	layout := &Layout{Overrides: []Override{{Path: "/", UID: &root, GID: &root, Recursive: true}}}
	entries, err := Walk(srcRoot, layout)
	if err != nil {
		return nil, err
	}
	script, err := DebugfsScript(entries)
	if err != nil {
		return nil, err
	}
	var total int64
	_ = filepath.WalkDir(srcRoot, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	f, err := os.Create(outImage)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate((total*12/10 + 64<<20) &^ (1<<20 - 1)); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if out, err := sys.Run(ctx, sbinTool("mke2fs"), "-q", "-F", "-t", "ext4", "-O", "^has_journal", "-m", "0", "-N", fmt.Sprint(len(entries)+4096), "-L", label, "-d", srcRoot, outImage); err != nil {
		return nil, fmt.Errorf("mke2fs %s: %v: %s", label, err, out)
	}
	sf, err := os.CreateTemp("", "evh-layer-*.debugfs")
	if err != nil {
		return nil, err
	}
	defer os.Remove(sf.Name())
	if _, err := sf.WriteString(script); err != nil {
		return nil, err
	}
	sf.Close()
	out, _ := sys.Run(ctx, sbinTool("debugfs"), "-w", "-f", sf.Name(), outImage)
	if strings.Contains(out, "File not found") {
		return nil, fmt.Errorf("debugfs ownership pass failed: %s", firstErrLine(out))
	}
	return entries, nil
}

// WriteEntries saves entries in the format LoadEntries reads.
func WriteEntries(path string, entries []Entry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(map[string]any{"path": e.Path, "kind": string(e.Kind), "perm": fmt.Sprintf("%04o", e.Perm), "uid": e.UID, "gid": e.GID, "sha256": e.SHA256, "link": e.Link}); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}
