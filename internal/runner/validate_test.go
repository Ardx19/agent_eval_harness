package runner

import "testing"

func TestEmptyRunVerdict(t *testing.T) {
	pass := map[string]string{"a": "PASSED", "b": "PASSED"}
	mixed := map[string]string{"a": "PASSED", "b": "FAILED"}
	missing := map[string]string{"a": "MISSING"}
	cases := []struct {
		name         string
		doNothingOK  bool
		f2p          map[string]string
		wantResolved bool
		wantProblem  bool
	}{
		{"normal task, untouched workspace fails: good", false, mixed, false, false},
		{"normal task, nothing passes: good", false, missing, false, false},
		{"normal task already solved: invalid", false, pass, true, true},
		{"do-nothing task passes untouched: good", true, pass, true, false},
		{"do-nothing task fails untouched: invalid", true, mixed, false, true},
	}
	for _, c := range cases {
		got, problem := emptyRunVerdict(c.doNothingOK, c.f2p)
		if got != c.wantResolved || (problem != "") != c.wantProblem {
			t.Errorf("%s: resolved=%v problem=%q", c.name, got, problem)
		}
	}
}
