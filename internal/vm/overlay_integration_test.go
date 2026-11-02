package vm

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"agentevalharness/internal/sandbox"
)

func sha(s string) string {
	f := filepath.Join(os.TempDir(), "evh-sha-tmp")
	defer os.Remove(f)
	os.WriteFile(f, []byte(s), 0o600)
	h, _ := sandbox.Sha256File(f)
	return h
}

// Boots a guest that switches to an overlay root (scratch disk over workspace
// layer over the read-only base), performs file operations as the agent, the
// tool uid and root, then checks the host-side diff against exactly what ran.
func TestOverlayDiffEndToEnd(t *testing.T) {
	r := needRig(t)
	ctx := context.Background()
	sys := sandbox.NewSystem()

	src := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("existing.txt", "v1\n")
	put("delete_me.txt", "bye\n")
	put("keep.txt", "same\n")
	put("replace_dir/old.txt", "old\n")
	wsImg := filepath.Join(t.TempDir(), "workspace.ext4")
	wsEntries, err := sandbox.BuildWorkspace(ctx, sys, src, wsImg, sandbox.DefaultIdentities)
	if err != nil {
		t.Fatal(err)
	}
	baseEntries, err := sandbox.LoadEntries(filepath.Join("..", "..", "images", "build", "entries.jsonl"))
	if err != nil {
		t.Fatalf("load base entries (run images/build.sh): %v", err)
	}
	lower := sandbox.NewLower(baseEntries, wsEntries)

	baseBefore := ""
	if !testing.Short() {
		baseBefore, _ = sandbox.Sha256File(r.base)
	}
	cfg := r.cfg(t, "/overlay-selftest")
	cfg.WorkspaceImage, cfg.ScratchMiB, cfg.MemMiB = wsImg, 64, 512
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Start(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := v.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	console := v.Console()
	if res.Exit != ExitOK || !strings.Contains(console, "SPIKE: DONE") {
		t.Fatalf("guest did not complete (exit %s). If the image predates U4, rerun images/build.sh.\n%s", res.Exit, console)
	}
	if strings.Contains(console, "FAIL") || strings.Contains(console, "actions failed") {
		t.Fatalf("guest reported a failure:\n%s", console)
	}

	if err := sandbox.CheckScratch(ctx, sys, filepath.Join(cfg.RunDir, "scratch.img")); err != nil {
		t.Fatal(err)
	}
	if baseBefore != "" {
		if after, _ := sandbox.Sha256File(r.base); after != baseBefore {
			t.Fatal("the shared base image changed during the trial")
		}
	}
	changes, err := sandbox.ExtractDiff(ctx, sys, filepath.Join(cfg.RunDir, "scratch.img"), lower, sandbox.DefaultIdentities)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]sandbox.Change{}
	for _, c := range changes {
		got[c.Path] = c
	}
	const P = "/home/dev/project/"
	type want struct {
		op, kind, writer string
		changed          *bool
		derived          bool
	}
	yes, no := true, false
	expect := map[string]want{
		P + "new.txt":             {"create", "file", "agent", &yes, false},
		P + "existing.txt":        {"modify", "file", "agent", &yes, false},
		P + "delete_me.txt":       {"delete", "file", "", nil, false},
		P + "keep.txt":            {"modify", "file", "agent", &no, false},
		P + "replace_dir":         {"replace", "dir", "agent", nil, false},
		P + "replace_dir/new.txt": {"create", "file", "agent", &yes, false},
		P + "replace_dir/old.txt": {"delete", "file", "", nil, true},
		P + "sub":                 {"create", "dir", "agent", nil, false},
		P + "sub/dir":             {"create", "dir", "agent", nil, false},
		P + "sub/dir/deep.txt":    {"create", "file", "agent", &yes, false},
		P + "link":                {"create", "symlink", "agent", &yes, false},
		"/tmp/agent-tmp.txt":      {"create", "file", "agent", &yes, false},
		"/tmp/tool-out.json":      {"create", "file", "tool", &yes, false},
		"/etc/hosts":              {"modify", "file", "root", &yes, false},
		"/etc/shells":             {"delete", "file", "", nil, false},
	}
	for p, w := range expect {
		c, ok := got[p]
		if !ok {
			t.Errorf("missing change: %s", p)
			continue
		}
		if c.Op != w.op || c.Kind != w.kind || c.Derived != w.derived {
			t.Errorf("%s: got op=%s kind=%s derived=%v want op=%s kind=%s derived=%v", p, c.Op, c.Kind, c.Derived, w.op, w.kind, w.derived)
		}
		if w.changed != nil && c.ContentChanged != *w.changed {
			t.Errorf("%s: content_changed=%v want %v", p, c.ContentChanged, *w.changed)
		}
		if w.writer != "" && c.WriterHint != w.writer {
			t.Errorf("%s: writer=%s want %s", p, c.WriterHint, w.writer)
		}
	}
	var extra []string
	for p := range got {
		if _, ok := expect[p]; !ok {
			extra = append(extra, p+" ("+got[p].Op+")")
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("unexpected changes (noise from the guest setup would make F5 unreliable): %v", extra)
	}
	if c := got[P+"existing.txt"]; c.SHA256 != sha("v1\nmore\n") {
		t.Errorf("existing.txt content hash wrong: %+v", c)
	}
	if c := got["/etc/hosts"]; !strings.Contains(c.Path, "hosts") || c.UID != 0 {
		t.Errorf("hosts: %+v", c)
	}
	t.Logf("%d changes extracted; guest console:\n%s", len(changes), console)
}
