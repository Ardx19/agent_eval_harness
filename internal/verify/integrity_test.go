package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var canary = Canary{File: "test_evh_abc.py", Fail: "test_evh_a_abc", Pass: "test_evh_b_abc"}

func honestLines() []string {
	return []string{
		"PASSED testing/test_x.py::test_one",
		"PASSED testing/test_x.py::test_two",
		"PASSED test_evh_abc.py::test_evh_b_abc",
		"FAILED test_evh_abc.py::test_evh_a_abc - AssertionError: must fail",
		"========================= 1 failed, 3 passed in 0.12s =========================",
	}
}

func TestParseSummary(t *testing.T) {
	for in, want := range map[string]map[string]int{
		"=== 1 failed, 3 passed in 0.12s ===":                          {"failed": 1, "passed": 3},
		"79 passed in 3.08s":                                           {"passed": 79},
		"2 failed, 77 passed, 3 skipped, 1 xfailed in 2.97s (0:00:02)": {"failed": 2, "passed": 77, "skipped": 3, "xfailed": 1},
		"1 error in 0.18s":                                             {"error": 1},
	} {
		got, ok := ParseSummary([]string{"noise", in})
		if !ok || len(got) != len(want) {
			t.Errorf("%q: %v %v", in, got, ok)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%q: %s=%d want %d", in, k, got[k], v)
			}
		}
	}
	if _, ok := ParseSummary([]string{"PASSED a::b"}); ok {
		t.Error("no summary line must be reported as missing")
	}
}

func TestIntegrityAcceptsAnHonestRun(t *testing.T) {
	l := honestLines()
	in := CheckIntegrity(l, ParseOutcomes(l), canary, 1, []string{"testing/test_x.py"})
	if !in.OK || !in.CanaryOK {
		t.Fatalf("%+v", in)
	}
}

func TestIntegrityCatchesForgery(t *testing.T) {
	targets := []string{"testing/test_x.py"}
	cases := map[string]struct {
		lines []string
		exit  int
		want  string
	}{
		"every failure reported as passed": {[]string{
			"PASSED testing/test_x.py::test_one", "PASSED test_evh_abc.py::test_evh_a_abc", "PASSED test_evh_abc.py::test_evh_b_abc",
			"==== 3 passed in 0.1s ===="}, 0, "must fail"},
		"exit code forced to zero": {[]string{"PASSED testing/test_x.py::test_one", "FAILED test_evh_abc.py::test_evh_a_abc - x", "1 failed, 1 passed in 0.1s"}, 0, "exited 0"},
		"fake lines printed by the code under test": {append(honestLines()[:1:1], "PASSED testing/test_x.py::test_secret_hidden", "PASSED testing/test_x.py::test_secret_hidden2",
			"FAILED test_evh_abc.py::test_evh_a_abc - x", "1 failed, 1 passed in 0.1s"), 1, "summary counts"},
		"no summary at all": {[]string{"PASSED testing/test_x.py::test_one", "FAILED test_evh_abc.py::test_evh_a_abc - x"}, 1, "no summary line"},
	}
	for name, c := range cases {
		in := CheckIntegrity(c.lines, ParseOutcomes(c.lines), canary, c.exit, targets)
		if in.OK || !strings.Contains(in.Reason, c.want) {
			t.Errorf("%s: %+v", name, in)
		}
	}
}

// Output printed by tests that run pytest inside pytest (captured stdout) looks like
// report lines but belongs to other files, so it must not trip the count check.
func TestIntegrityIgnoresNestedPytestOutput(t *testing.T) {
	l := append([]string{"PASSED test_inner.py::test_nested", "FAILED test_inner.py::test_nested2 - assert 0"}, honestLines()...)
	if in := CheckIntegrity(l, ParseOutcomes(l), canary, 1, []string{"testing/test_x.py"}); !in.OK {
		t.Fatalf("%+v", in)
	}
}

func TestAddCanary(t *testing.T) {
	dir := t.TempDir()
	a, err := AddCanary(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := AddCanary(t.TempDir())
	if a.File == b.File {
		t.Fatal("canary names must be random per run")
	}
	src, _ := os.ReadFile(filepath.Join(dir, a.File))
	if !strings.Contains(string(src), "def "+a.Fail) || !strings.Contains(string(src), "assert False") {
		t.Fatalf("%s", src)
	}
}
