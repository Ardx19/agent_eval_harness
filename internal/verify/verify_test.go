package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agentevalharness/internal/sandbox"
)

func TestParseOutcomes(t *testing.T) {
	got := ParseOutcomes([]string{
		"PASSED testing/a.py::test_ok",
		"PASSED testing/a.py::test_p[x-1]",
		"FAILED testing/a.py::test_bad - assert 1 == 2",
		"XFAIL testing/a.py::test_xf",
		"PASSED testing/a.py::test_both",
		"ERROR testing/a.py::test_both - teardown",
		"PASSED testing/a.py::test_sp[a b-c d]",
		"some other line",
		"=== short test summary info ===",
	})
	want := map[string]string{
		"testing/a.py::test_ok": "PASSED", "testing/a.py::test_p[x-1]": "PASSED", "testing/a.py::test_bad": "FAILED",
		"testing/a.py::test_xf": "PASSED", "testing/a.py::test_both": "FAILED",
		"testing/a.py::test_sp[a": "PASSED",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestJudge(t *testing.T) {
	tests := &Tests{FailToPass: []string{"a::f"}, PassToPass: []string{"a::p1", "a::p2"}}
	cases := []struct {
		name  string
		got   map[string]string
		empty bool
		want  string
		cause string
	}{
		{"all pass", map[string]string{"a::f": "PASSED", "a::p1": "PASSED", "a::p2": "PASSED"}, false, "resolved", ""},
		{"f2p fails", map[string]string{"a::f": "FAILED", "a::p1": "PASSED", "a::p2": "PASSED"}, false, "unresolved", "f2p_failed"},
		{"f2p missing", map[string]string{"a::p1": "PASSED", "a::p2": "PASSED"}, false, "unresolved", "f2p_failed"},
		{"regression", map[string]string{"a::f": "PASSED", "a::p1": "FAILED", "a::p2": "PASSED"}, false, "unresolved", "p2p_broken"},
		{"empty patch", map[string]string{"a::f": "FAILED"}, true, "unresolved", "empty_patch"},
	}
	for _, c := range cases {
		v := Judge(tests, c.got, c.empty)
		if v.Status != c.want || v.Cause != c.cause || v.Resolved != (c.want == "resolved") {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

func TestTampering(t *testing.T) {
	got := tampering([]string{"src/x.py", "conftest.py", "testing/t.py", "tests/sub/conftest.py", "evil.pth"}, []string{"testing/t.py"})
	want := []string{"conftest.py", "testing/t.py", "tests/sub/conftest.py", "evil.pth"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The agent's final files go onto the pristine repo, its edits to test files are
// discarded in favour of the hidden test patch, and noise is ignored.
func TestBuildTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	os.MkdirAll(filepath.Join(repo, "tests"), 0o755)
	os.WriteFile(filepath.Join(repo, "pkg/mod.py"), []byte("def f():\n    return 1\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "pkg/old.py"), []byte("x = 1\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "tests/test_mod.py"), []byte("def test_f():\n    assert 1\n"), 0o644)
	mustGit(t, repo, "init", "-q", "-b", "main")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "init")

	// the hidden test patch, made from a real edit of the test file
	scratch := filepath.Join(t.TempDir(), "s")
	exec.Command("cp", "-a", repo, scratch).Run()
	os.WriteFile(filepath.Join(scratch, "tests/test_mod.py"), []byte("def test_f():\n    assert 1\n\ndef test_g():\n    assert 2\n"), 0o644)
	patch, err := exec.Command("git", "-C", scratch, "diff").Output()
	if err != nil || len(patch) == 0 {
		t.Fatal("could not make test patch", err)
	}

	// the agent's upper layer: fixes mod.py, adds a file, deletes old.py, edits the test file
	upper := t.TempDir()
	proj := filepath.Join(upper, "upper/home/dev/project")
	os.MkdirAll(filepath.Join(proj, "pkg/__pycache__"), 0o755)
	os.MkdirAll(filepath.Join(proj, "tests"), 0o755)
	os.WriteFile(filepath.Join(proj, "pkg/mod.py"), []byte("def f():\n    return 2\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "pkg/new.py"), []byte("y = 2\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "pkg/__pycache__/mod.pyc"), []byte("junk"), 0o644)
	os.WriteFile(filepath.Join(proj, "tests/test_mod.py"), []byte("def test_f():\n    assert True  # cheated\n"), 0o644)
	ch := []sandbox.Change{
		{Path: "/home/dev/project/pkg/mod.py", Op: "modify", Kind: "file", Perm: 0o644, ContentChanged: true},
		{Path: "/home/dev/project/pkg/new.py", Op: "create", Kind: "file", Perm: 0o644, ContentChanged: true},
		{Path: "/home/dev/project/pkg/old.py", Op: "delete", Kind: "file"},
		{Path: "/home/dev/project/pkg/__pycache__/mod.pyc", Op: "create", Kind: "file", Perm: 0o644, ContentChanged: true},
		{Path: "/home/dev/project/tests/test_mod.py", Op: "modify", Kind: "file", Perm: 0o644, ContentChanged: true},
		{Path: "/home/dev/.bash_history", Op: "create", Kind: "file", Perm: 0o600, ContentChanged: true},
	}
	wd, _ := os.Getwd()
	rel, _ := filepath.Rel(wd, filepath.Join(t.TempDir(), "tree")) // a relative destination, as the harness's --out can be
	dst := rel
	spec := Spec{Tests: &Tests{TestFiles: []string{"tests/test_mod.py"}}, TestPatch: patch, RepoDir: repo, UpperDir: upper, Changes: ch}
	model, files, err := BuildTree(spec, dst)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pkg/mod.py", "pkg/new.py", "pkg/old.py", "tests/test_mod.py"}; !reflect.DeepEqual(files, want) {
		// the test file shows in the agent's own patch; it is reset afterwards
		t.Fatalf("changed files %v", files)
	}
	if !strings.Contains(string(model), "return 2") || strings.Contains(string(model), "pyc") {
		t.Fatalf("patch: %s", model)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(wd, dst, p)); return string(b) }
	if read("pkg/mod.py") != "def f():\n    return 2\n" || read("pkg/new.py") != "y = 2\n" {
		t.Error("the agent's source files must be in the tree")
	}
	if _, err := os.Stat(filepath.Join(wd, dst, "pkg/old.py")); err == nil {
		t.Error("a file the agent deleted must be gone")
	}
	if got := read("tests/test_mod.py"); !strings.Contains(got, "test_g") || strings.Contains(got, "cheated") {
		t.Errorf("test file must be the base plus the hidden patch, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(wd, dst, "pkg/__pycache__/mod.pyc")); err == nil {
		t.Error("noise must not be copied")
	}
	if got := tampering(files, spec.Tests.TestFiles); !reflect.DeepEqual(got, []string{"tests/test_mod.py"}) {
		t.Errorf("tampering %v", got)
	}
}

func TestRunTargetsSkipsDataFiles(t *testing.T) {
	tt := &Tests{TestFiles: []string{"tests/static/config.toml", "tests/test_config.py", "tests/helpers.py", "tests/conftest.py"}}
	if got := tt.RunTargets(); !reflect.DeepEqual(got, []string{"tests/test_config.py"}) {
		t.Fatalf("got %v", got)
	}
	only := &Tests{TestFiles: []string{"testing/check.py", "data.txt"}}
	if got := only.RunTargets(); !reflect.DeepEqual(got, []string{"testing/check.py"}) {
		t.Fatalf("fallback: %v", got)
	}
}
