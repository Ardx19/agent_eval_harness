package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needE2fs(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"mke2fs", "debugfs"} {
		if _, err := os.Stat(sbinTool(tool)); err != nil {
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("%s not available", tool)
			}
		}
	}
}

func sh(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// buildUpper makes an image whose /upper looks like an overlay upper layer:
// creates, modifies, a whiteout (char dev 0:0), an opaque directory, a symlink
// and a tool-owned file.
func buildUpper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree", "upper")
	w := func(rel, body string) {
		p := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("new.txt", "brand new\n")
	w("mod.txt", "changed\n")
	w("same.txt", "same\n")
	w("tool.txt", "from tool\n")
	w("newdir/f.txt", "f\n")
	w("repl/n.txt", "n\n")
	w("sub/dir/deep.txt", "deep\n")
	if err := os.Symlink("new.txt", filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "scratch.img")
	f, _ := os.Create(img)
	f.Truncate(16 << 20)
	f.Close()
	sh(t, sbinTool("mke2fs"), "-q", "-F", "-t", "ext4", "-O", "^has_journal", "-d", filepath.Join(dir, "tree"), img)
	script := "cd /upper\nmknod gone c 0 0\nea_set /upper/repl trusted.overlay.opaque y\nsif /upper/tool.txt uid 2000\nsif /upper/tool.txt gid 2000\nsif /upper/new.txt uid 1000\n"
	sf := filepath.Join(dir, "mk.debugfs")
	os.WriteFile(sf, []byte(script), 0o600)
	sh(t, sbinTool("debugfs"), "-w", "-f", sf, img)
	return img
}

func lowerForUpper() Lower {
	return NewLower([]Entry{
		{Path: "/mod.txt", Kind: 'f', SHA256: sum([]byte("old\n"))},
		{Path: "/same.txt", Kind: 'f', SHA256: sum([]byte("same\n"))},
		{Path: "/gone", Kind: 'f', SHA256: sum([]byte("x"))},
		{Path: "/repl", Kind: 'd'},
		{Path: "/repl/old.txt", Kind: 'f', SHA256: sum([]byte("o"))},
		{Path: "/sub", Kind: 'd'},
		{Path: "/sub/dir", Kind: 'd'},
	})
}

func TestExtractDiffClassifiesEverything(t *testing.T) {
	needE2fs(t)
	img := buildUpper(t)
	changes, err := ExtractDiff(context.Background(), NewSystem(), img, lowerForUpper(), DefaultIdentities)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Path] = c
	}
	type want struct {
		op, kind string
		changed  *bool
		writer   string
		derived  bool
	}
	yes, no := true, false
	expect := map[string]want{
		"/new.txt":          {"create", "file", &yes, "agent", false},
		"/mod.txt":          {"modify", "file", &yes, "", false},
		"/same.txt":         {"modify", "file", &no, "", false},
		"/tool.txt":         {"create", "file", &yes, "tool", false},
		"/gone":             {"delete", "file", nil, "", false},
		"/link":             {"create", "symlink", &yes, "", false},
		"/newdir":           {"create", "dir", nil, "", false},
		"/newdir/f.txt":     {"create", "file", &yes, "", false},
		"/repl":             {"replace", "dir", nil, "", false},
		"/repl/n.txt":       {"create", "file", &yes, "", false},
		"/repl/old.txt":     {"delete", "file", nil, "", true},
		"/sub/dir/deep.txt": {"create", "file", &yes, "", false},
	}
	for p, w := range expect {
		c, ok := got[p]
		if !ok {
			t.Errorf("missing change for %s", p)
			continue
		}
		if c.Op != w.op || c.Kind != w.kind || c.Derived != w.derived {
			t.Errorf("%s: got op=%s kind=%s derived=%v, want op=%s kind=%s derived=%v", p, c.Op, c.Kind, c.Derived, w.op, w.kind, w.derived)
		}
		if w.changed != nil && c.ContentChanged != *w.changed {
			t.Errorf("%s: content_changed=%v want %v", p, c.ContentChanged, *w.changed)
		}
		if w.writer != "" && c.WriterHint != w.writer {
			t.Errorf("%s: writer=%s want %s", p, c.WriterHint, w.writer)
		}
	}
	for p := range got {
		if _, ok := expect[p]; !ok {
			t.Errorf("unexpected change reported: %s (%s)", p, got[p].Op)
		}
	}
	if _, ok := got["/sub"]; ok {
		t.Error("existing copy-up container directories must not be reported")
	}
	if got["/new.txt"].SHA256 != sum([]byte("brand new\n")) || got["/new.txt"].Size != 10 {
		t.Errorf("sha/size wrong: %+v", got["/new.txt"])
	}
	if got["/link"].Link != "new.txt" {
		t.Errorf("symlink target: %+v", got["/link"])
	}
}

func TestExtractDiffErrorsWithoutUpperDir(t *testing.T) {
	needE2fs(t)
	img := filepath.Join(t.TempDir(), "empty.img")
	f, _ := os.Create(img)
	f.Truncate(8 << 20)
	f.Close()
	if err := FormatExt4(context.Background(), NewSystem(), img, "scratch"); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractDiff(context.Background(), NewSystem(), img, Lower{}, DefaultIdentities); err == nil {
		t.Fatal("a scratch image with no /upper must be an error, not an empty diff")
	}
}

func TestParseListing(t *testing.T) {
	out := `debugfs 1.46.5 (30-Dec-2021)
debugfs:  ls -l -p "/upper"
/12/040755/1000/1000/.//
/2/040755/0/0/..//
/14/100644/1000/1000/new.txt/6/
/19/020000/0/0/gone/0/
debugfs:  ea_list "/upper/repl"
Extended attributes:
  trusted.overlay.opaque (1) = "y"
`
	l, op := parseListing(out)
	if len(l["/"]) != 2 || l["/"][1].name != "gone" || l["/"][1].mode != 020000 {
		t.Fatalf("listing: %+v", l)
	}
	if !op["/repl"] || op["/"] {
		t.Fatalf("opaque: %v", op)
	}
}

func TestBuildWorkspace(t *testing.T) {
	needE2fs(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello\n"), 0o644)
	os.MkdirAll(filepath.Join(src, "d"), 0o755)
	os.WriteFile(filepath.Join(src, "d", "b.txt"), []byte("b\n"), 0o600)
	img := filepath.Join(t.TempDir(), "ws.ext4")
	entries, err := BuildWorkspace(context.Background(), NewSystem(), src, img, DefaultIdentities)
	if err != nil {
		t.Fatal(err)
	}
	lower := NewLower(entries)
	if e := lower["/home/dev/project/a.txt"]; e.UID != 1000 || e.SHA256 != sum([]byte("hello\n")) {
		t.Fatalf("entry: %+v", e)
	}
	bad := VerifyImage(context.Background(), NewSystem(), img, []Invariant{
		{Path: "/home", UID: 0, GID: 0, Mode: "0755"},
		{Path: "/home/dev", UID: 1000, GID: 1000, Mode: "0750"},
		{Path: "/home/dev/project", UID: 1000, GID: 1000, Mode: "0755"},
		{Path: "/home/dev/project/a.txt", UID: 1000, GID: 1000, Mode: "0644"},
		{Path: "/home/dev/project/d/b.txt", UID: 1000, GID: 1000, Mode: "0600"},
	})
	if len(bad) > 0 {
		t.Fatalf("workspace image ownership wrong: %v", bad)
	}
}

func TestFormatExt4(t *testing.T) {
	needE2fs(t)
	img := filepath.Join(t.TempDir(), "s.img")
	f, _ := os.Create(img)
	f.Truncate(8 << 20)
	f.Close()
	if err := FormatExt4(context.Background(), NewSystem(), img, "scratch"); err != nil {
		t.Fatal(err)
	}
	out, _ := NewSystem().Run(context.Background(), sbinTool("debugfs"), "-R", "stats", img)
	if !strings.Contains(out, "scratch") {
		t.Fatalf("label missing from fs stats:\n%.300s", out)
	}
}

func TestCheckScratchDetectsFullDisk(t *testing.T) {
	needE2fs(t)
	dir := t.TempDir()
	mk := func(name string, fileBytes int) string {
		tree := filepath.Join(dir, name+"-tree")
		os.MkdirAll(tree, 0o755)
		if fileBytes > 0 {
			os.WriteFile(filepath.Join(tree, "big"), bytes.Repeat([]byte{0xAB}, fileBytes) /* non-zero: mke2fs -d turns zero blocks into holes */, 0o644)
		}
		img := filepath.Join(dir, name+".img")
		f, _ := os.Create(img)
		f.Truncate(16 << 20)
		f.Close()
		sh(t, sbinTool("mke2fs"), "-q", "-F", "-t", "ext4", "-O", "^has_journal", "-m", "0", "-d", tree, img)
		return img
	}
	if err := CheckScratch(context.Background(), NewSystem(), mk("roomy", 1<<20)); err != nil {
		t.Fatalf("a mostly empty disk must pass: %v", err)
	}
	err := CheckScratch(context.Background(), NewSystem(), mk("full", 15_600_000))
	if !errors.Is(err, ErrScratchFull) {
		t.Fatalf("expected ErrScratchFull, got %v", err)
	}
}
