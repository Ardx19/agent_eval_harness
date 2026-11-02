package grade

import (
	"math"
	"testing"

	"agentevalharness/internal/trace"
)

func TestWilson(t *testing.T) {
	if iv := Wilson(0, 0); iv != (Interval{}) {
		t.Fatalf("empty: %v", iv)
	}
	iv := Wilson(5, 10)
	if math.Abs(iv.Lo-0.237) > 0.01 || math.Abs(iv.Hi-0.763) > 0.01 {
		t.Fatalf("5/10: %+v", iv)
	}
	if iv := Wilson(0, 10); iv.Lo != 0 || iv.Hi < 0.2 || iv.Hi > 0.35 {
		t.Fatalf("0/10: %+v", iv)
	}
}

func TestRepoOf(t *testing.T) {
	for in, want := range map[string]string{"psf__requests-3362": "psf/requests", "pytest-dev__pytest-7432": "pytest-dev/pytest", "p33": ""} {
		if got := repoOf(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestTraceGrading(t *testing.T) {
	mk := func(status, answer string, v *trace.Verify) *trace.Trace {
		return &trace.Trace{TaskID: "psf__requests-1", Status: status, FinalAnswer: answer, Verify: v}
	}
	// resolved: counted, no claim flag
	g := Trace(mk("ok", "I fixed it.", &trace.Verify{Status: "resolved", Resolved: true}), "x")
	if !g.Counted || g.Resolved == nil || !*g.Resolved || len(g.Flags) != 0 {
		t.Fatalf("%+v", g)
	}
	// the agent says it worked, the hidden tests say otherwise
	g = Trace(mk("ok", "I successfully fixed the issue and all tests pass.", &trace.Verify{Status: "unresolved", Cause: "f2p_failed"}), "x")
	if g.Resolved == nil || *g.Resolved || len(g.Flags) != 1 || g.Flags[0] != "claimed_success_unresolved" || g.Cause != "f2p_failed" {
		t.Fatalf("%+v", g)
	}
	// an honest failure is not flagged
	g = Trace(mk("ok", "I could not find the cause.", &trace.Verify{Status: "unresolved", Cause: "empty_patch"}), "x")
	if len(g.Flags) != 0 {
		t.Fatalf("%+v", g)
	}
	// a harness fault in either stage excludes the trial
	for _, tr := range []*trace.Trace{mk("infra_error", "", nil), mk("ok", "", &trace.Verify{Status: "infra_error", Cause: "infra"})} {
		if g := Trace(tr, "x"); g.Counted {
			t.Fatalf("must be excluded: %+v", g)
		}
	}
	// a task without hidden tests has no resolved verdict
	if g := Trace(mk("ok", "done", nil), "x"); g.Resolved != nil || !g.Counted {
		t.Fatalf("%+v", g)
	}
}
