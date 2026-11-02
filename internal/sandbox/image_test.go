package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ip(i int) *int { return &i }

func mkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "usr/local/bin"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "opt/svc/data"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "home/dev"), 0o755))
	must(os.WriteFile(filepath.Join(root, "usr/local/bin/toolstub"), []byte("stub"), 0o755))
	must(os.WriteFile(filepath.Join(root, "opt/svc/data/model.bin"), []byte("data"), 0o644))
	must(os.WriteFile(filepath.Join(root, "home/dev/notes.txt"), []byte("hi"), 0o644))
	must(os.Symlink("toolstub", filepath.Join(root, "usr/local/bin/alias")))
	return root
}

func testLayout() *Layout {
	return &Layout{Overrides: []Override{
		{Path: "/home/dev", UID: ip(1000), GID: ip(1000), Recursive: true},
		{Path: "/usr/local/bin/toolstub", UID: ip(2000), GID: ip(2000), Mode: "04711"},
		{Path: "/opt/svc", UID: ip(2000), GID: ip(2000), Mode: "0700", Recursive: true},
	}}
}

func find(es []Entry, p string) Entry {
	for _, e := range es {
		if e.Path == p {
			return e
		}
	}
	return Entry{}
}

func TestWalkAppliesOwnershipAndModes(t *testing.T) {
	es, err := Walk(mkTree(t), testLayout())
	if err != nil {
		t.Fatal(err)
	}
	if e := find(es, "/etc"); e.Path != "" {
		t.Fatal("unexpected /etc")
	}
	if e := find(es, "/usr"); e.UID != 0 || e.GID != 0 {
		t.Fatalf("default owner should be root: %+v", e)
	}
	stub := find(es, "/usr/local/bin/toolstub")
	if stub.UID != 2000 || stub.Perm != 04711 || !stub.ModeOverridden {
		t.Fatalf("stub: %+v", stub)
	}
	if e := find(es, "/opt/svc/data/model.bin"); e.UID != 2000 {
		t.Fatalf("recursive uid not applied: %+v", e)
	}
	if e := find(es, "/opt/svc/data/model.bin"); e.ModeOverridden {
		t.Fatal("mode override must not recurse")
	}
	if e := find(es, "/home/dev/notes.txt"); e.UID != 1000 || e.GID != 1000 {
		t.Fatalf("home owner: %+v", e)
	}
	if l := find(es, "/usr/local/bin/alias"); l.Kind != 'l' || l.Link != "toolstub" {
		t.Fatalf("symlink: %+v", l)
	}
}

func TestWalkRejectsMissingOverridePath(t *testing.T) {
	l := &Layout{Overrides: []Override{{Path: "/does/not/exist", UID: ip(1)}}}
	if _, err := Walk(mkTree(t), l); err == nil {
		t.Fatal("expected error for missing override path")
	}
}

func TestTreeHashStableAndSensitive(t *testing.T) {
	root := mkTree(t)
	a, _ := Walk(root, testLayout())
	b, _ := Walk(root, testLayout())
	if TreeHash(a) != TreeHash(b) {
		t.Fatal("hash not stable across walks")
	}
	l2 := testLayout()
	l2.Overrides[1].Mode = "0755"
	c, _ := Walk(root, l2)
	if TreeHash(a) == TreeHash(c) {
		t.Fatal("hash must change when a mode override changes")
	}
	if err := os.WriteFile(filepath.Join(root, "home/dev/notes.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := Walk(root, testLayout())
	if TreeHash(a) == TreeHash(d) {
		t.Fatal("hash must change when content changes")
	}
}

func TestPrepareRemovesAndRefusesRoot(t *testing.T) {
	root := mkTree(t)
	removed, err := Prepare(root, &Layout{Remove: []string{"/usr/local/bin/alias", "/nope"}})
	if err != nil || len(removed) != 1 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, err := Prepare(root, &Layout{Remove: []string{"/"}}); err == nil {
		t.Fatal("must refuse to remove /")
	}
}

func TestDebugfsScript(t *testing.T) {
	es, _ := Walk(mkTree(t), testLayout())
	s, err := DebugfsScript(es)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`sif "/usr/local/bin/toolstub" uid 2000`,
		`sif "/usr/local/bin/toolstub" mode 0104711`,
		`sif "/opt/svc" mode 040700`,
		`sif "/usr" uid 0`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q", want)
		}
	}
	if _, err := DebugfsScript([]Entry{{Path: "/bad\"name"}}); err == nil {
		t.Fatal("must reject unsafe paths")
	}
	// systemd ships a unit literally named system-systemd\x2dcryptsetup.slice
	s2, err := DebugfsScript([]Entry{{Path: `/usr/lib/systemd/system/system-systemd\x2dcryptsetup.slice`}})
	if err != nil || !strings.Contains(s2, `"/usr/lib/systemd/system/system-systemd\x2dcryptsetup.slice" uid 0`) {
		t.Fatalf("backslash path must pass through unchanged: %v %q", err, s2)
	}
}

const statSample = `Inode: 1234   Type: regular    Mode:  4711   Flags: 0x0
Generation: 0    Version: 0x00000000
User:  2000   Group:  2000   Project:     0   Size: 4
`

func TestParseStat(t *testing.T) {
	perm, uid, gid, err := ParseStat(statSample)
	if err != nil || perm != 04711 || uid != 2000 || gid != 2000 {
		t.Fatalf("perm=%o uid=%d gid=%d err=%v", perm, uid, gid, err)
	}
	if _, _, _, err := ParseStat("garbage"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestVerifyImage(t *testing.T) {
	sys := &fakeSys{run: map[string]fakeRun{
		`debugfs -R stat "/usr/local/bin/toolstub" img`: {out: statSample},
		`debugfs -R stat "/opt/svc" img`:                {out: strings.Replace(strings.Replace(statSample, "4711", "755", 1), "2000", "0", 2)},
		`debugfs -R stat "/usr/bin/strace" img`:         {out: "stat: File not found by ext2_lookup", err: os.ErrNotExist},
		`debugfs -R stat "/usr/bin/id" img`:             {out: statSample},
	}}
	bad := VerifyImage(context.Background(), sys, "img", []Invariant{
		{Path: "/usr/local/bin/toolstub", UID: 2000, GID: 2000, Mode: "04711"},
		{Path: "/opt/svc", UID: 2000, GID: 2000, Mode: "0700"},
		{Path: "/usr/bin/strace", Absent: true},
		{Path: "/usr/bin/id", Absent: true},
		{Path: "/missing", UID: 0, GID: 0, Mode: "0755"},
	})
	if len(bad) != 3 {
		t.Fatalf("expected 3 failures, got %v", bad)
	}
}

// The shipped layout must keep the agent and tool identities separate and
// protect the tool directory; this guards the design's core invariants.
func TestShippedLayoutInvariants(t *testing.T) {
	l, _, err := LoadLayout("../../images/layout.json")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Invariant{}
	for _, inv := range l.Invariants {
		by[inv.Path] = inv
	}
	stub, ok := by["/usr/local/bin/toolstub"]
	if !ok || stub.Mode != "04711" || stub.UID != 2000 {
		t.Fatalf("toolstub must be setuid 04711 owned by the tool uid: %+v", stub)
	}
	svc, ok := by["/opt/svc"]
	if !ok || svc.Mode != "0700" || svc.UID != stub.UID {
		t.Fatalf("/opt/svc must be 0700 and tool-owned: %+v", svc)
	}
	home, ok := by["/home/dev"]
	if !ok || home.UID == stub.UID {
		t.Fatalf("agent uid must differ from the tool uid: %+v", home)
	}
	if passwd := by["/etc/passwd"]; passwd.UID != 0 || passwd.Mode != "0644" {
		t.Fatalf("/etc/passwd must be root-owned 0644: %+v", passwd)
	}
	if !by["/usr/bin/strace"].Absent {
		t.Fatal("strace must be removed from the image")
	}
}

// Runs only when an image has been built; then it checks the real thing.
func TestBuiltImageMatchesLayout(t *testing.T) {
	img := "../../images/build/rootfs.ext4"
	if _, err := os.Stat(img); err != nil {
		t.Skip("no built image; run images/build.sh")
	}
	l, _, err := LoadLayout("../../images/layout.json")
	if err != nil {
		t.Fatal(err)
	}
	if bad := VerifyImage(context.Background(), NewSystem(), img, l.Invariants); len(bad) > 0 {
		t.Fatalf("image violates layout: %v", bad)
	}
}
