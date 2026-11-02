package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentevalharness/internal/sandbox"
)

func needE2fs(t *testing.T) {
	t.Helper()
	for _, p := range []string{"/usr/sbin/mke2fs", "/usr/sbin/debugfs"} {
		if _, err := os.Stat(p); err != nil {
			if _, err := exec.LookPath(filepath.Base(p)); err != nil {
				t.Skip("e2fsprogs not available")
			}
		}
	}
}

func TestInstallLayout(t *testing.T) {
	m := loadRetro(t)
	byPath := map[string]sandbox.ExtraFile{}
	for _, f := range m.Install("doc body") {
		byPath[f.Path] = f
	}
	if len(byPath) != 3 {
		t.Fatalf("expected the entrypoint, the doc and the stub config: %+v", byPath)
	}
	ep := byPath["/home/dev/project/scripts/run_retrosynthesis.sh"]
	if ep.Symlink != "/usr/local/bin/toolstub" || !ep.RootOwned {
		t.Fatalf("entrypoint must be a root-owned symlink at the path the doc uses: %+v", ep)
	}
	doc := byPath["/usr/share/agent-skills/retrosynthesis/SKILL.md"]
	if doc.Mode != 0o444 || !doc.RootOwned {
		t.Fatalf("doc must be root-owned and read-only: %+v", doc)
	}
	conf := byPath["/usr/share/agent-skills/retrosynthesis/stub.json"]
	if !conf.RootOwned || string(conf.Data) != `{"argv0":"run_retrosynthesis.sh","file_flags":["--request"],"stdin":true}` {
		t.Fatalf("stub config: %+v %s", conf, conf.Data)
	}
	if strings.Contains(string(conf.Data), "/") && strings.Contains(string(conf.Data), "home") {
		t.Fatal("the stub config must not mention host paths")
	}
}

func TestKeyFileIsToolOwned(t *testing.T) {
	k := KeyFile([]byte("secret"))
	if k.Path != "/opt/svc/key" || k.Mode != 0o400 || k.Owner == nil || *k.Owner != 2000 || k.ParentMode != "0700" || k.RootOwned {
		t.Fatalf("%+v", k)
	}
}

// The agent can read and run the skill's files but cannot edit, replace or delete them.
func TestInstalledSkillIsProtectedInTheImage(t *testing.T) {
	needE2fs(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("task input\n"), 0o644)
	img := filepath.Join(t.TempDir(), "ws.ext4")
	m := loadRetro(t)
	extras := append(m.Install("# doc\n"), KeyFile([]byte("0123456789abcdef0123456789abcdef")))
	if _, err := sandbox.BuildWorkspaceWith(context.Background(), sandbox.NewSystem(), src, img, sandbox.DefaultIdentities, extras); err != nil {
		t.Fatal(err)
	}
	bad := sandbox.VerifyImage(context.Background(), sandbox.NewSystem(), img, []sandbox.Invariant{
		{Path: "/home/dev/project/notes.txt", UID: 1000, GID: 1000, Mode: "0644"},
		{Path: "/home/dev/project/scripts", UID: 0, GID: 0, Mode: "0755"}, // agent cannot delete entries
		{Path: "/home/dev/project/scripts/run_retrosynthesis.sh", UID: 0, GID: 0, Mode: "0777"},
		{Path: "/usr/share/agent-skills/retrosynthesis", UID: 0, GID: 0, Mode: "0755"},
		{Path: "/usr/share/agent-skills/retrosynthesis/SKILL.md", UID: 0, GID: 0, Mode: "0444"},
		{Path: "/usr/share/agent-skills/retrosynthesis/stub.json", UID: 0, GID: 0, Mode: "0444"},
		{Path: "/opt/svc", UID: 2000, GID: 2000, Mode: "0700"},     // only the tool uid can enter
		{Path: "/opt/svc/key", UID: 2000, GID: 2000, Mode: "0400"}, // only the tool uid can read
		{Path: "/home/dev", UID: 1000, GID: 1000, Mode: "0750"},
	})
	if len(bad) > 0 {
		t.Fatalf("installed layout wrong: %v", bad)
	}
}

func TestExtrasAreValidated(t *testing.T) {
	needE2fs(t)
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "scripts"), 0o755)
	os.WriteFile(filepath.Join(src, "scripts", "run_retrosynthesis.sh"), []byte("x"), 0o755)
	build := func(extras []sandbox.ExtraFile) error {
		_, err := sandbox.BuildWorkspaceWith(context.Background(), sandbox.NewSystem(), src, filepath.Join(t.TempDir(), "w.ext4"), sandbox.DefaultIdentities, extras)
		return err
	}
	if err := build(loadRetro(t).Install("d")); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("an extra that overwrites a task input must be refused: %v", err)
	}
	for _, p := range []string{"/etc/passwd", "/home/dev/../../etc/x", "relative/path", "/usr/local/bin/toolstub"} {
		if err := build([]sandbox.ExtraFile{{Path: p, Data: []byte("x")}}); err == nil {
			t.Errorf("path %q must be refused", p)
		}
	}
}
