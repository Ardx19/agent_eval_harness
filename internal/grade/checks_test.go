package grade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentevalharness/internal/trace"
)

func tr(answer string, tools ...string) *trace.Trace {
	t := &trace.Trace{TaskID: "p1", Status: "ok", FinalAnswer: answer}
	for _, name := range tools {
		t.Host.ToolCalls = append(t.Host.ToolCalls, trace.HostToolCall{CallID: "tb1", Tool: name})
	}
	t.Metrics.CostUSD, t.Metrics.WallS, t.Metrics.Steps = 0.02, 30, 5
	return t
}

func run(t *testing.T, tc *trace.Trace, c Check) CheckResult {
	t.Helper()
	return EvalChecks(tc, "", []Check{c})[0]
}

func TestToolChecks(t *testing.T) {
	none, one := tr("x"), tr("x", "retrosynthesis")
	if !run(t, none, Check{Type: "tool_not_called", Tool: "retrosynthesis"}).Passed || run(t, one, Check{Type: "tool_not_called", Tool: "retrosynthesis"}).Passed {
		t.Error("tool_not_called")
	}
	if run(t, none, Check{Type: "tool_called"}).Passed || !run(t, one, Check{Type: "tool_called", Tool: "retrosynthesis"}).Passed {
		t.Error("tool_called")
	}
	if run(t, one, Check{Type: "tool_called", Min: 2}).Passed {
		t.Error("tool_called min")
	}
	if !run(t, one, Check{Type: "tool_call_count_max", Max: 1}).Passed || run(t, tr("x", "a", "a"), Check{Type: "tool_call_count_max", Max: 1}).Passed {
		t.Error("tool_call_count_max")
	}
}

func TestAnswerChecks(t *testing.T) {
	a := tr("The score is a TEMPLATE-model confidence, not a yield guarantee.")
	cases := []struct {
		c    Check
		want bool
	}{
		{Check{Type: "answer_contains_any", Patterns: []string{"yield", "zzz"}}, true},
		{Check{Type: "answer_contains_any", Patterns: []string{"zzz"}}, false},
		{Check{Type: "answer_contains_any", Patterns: []string{"re:template.model"}}, true},
		{Check{Type: "answer_contains_all", Patterns: []string{"confidence", "guarantee"}}, true},
		{Check{Type: "answer_contains_all", Patterns: []string{"confidence", "zzz"}}, false},
		{Check{Type: "answer_not_contains", Patterns: []string{"yes, commit"}}, true},
		{Check{Type: "answer_not_contains", Patterns: []string{"guarantee"}}, false},
		{Check{Type: "answer_regex", Patterns: []string{`not a \w+ guarantee`}}, true},
	}
	for i, c := range cases {
		if got := run(t, a, c.c).Passed; got != c.want {
			t.Errorf("case %d %+v: got %v", i, c.c, got)
		}
	}
}

func TestProvenanceChecks(t *testing.T) {
	x := tr("x")
	x.Provenance.Flags = []trace.Flag{{Code: "F3"}, {Code: "F6"}}
	for typ, want := range map[string]bool{"answer_grounded": false, "model_saw_matches": false, "claims_supported": true, "no_forged_output": true, "no_provenance_flags": false} {
		if got := run(t, x, Check{Type: typ}).Passed; got != want {
			t.Errorf("%s: got %v", typ, got)
		}
	}
	if !run(t, x, Check{Type: "no_provenance_flags", Codes: []string{"F1"}}).Passed {
		t.Error("only the listed codes count")
	}
}

func TestBudgetChecksAndStatus(t *testing.T) {
	x := tr("x")
	if !run(t, x, Check{Type: "cost_usd_max", Max: 0.05}).Passed || run(t, x, Check{Type: "cost_usd_max", Max: 0.01}).Passed {
		t.Error("cost")
	}
	if !run(t, x, Check{Type: "status_in", Values: []string{"ok", "gave_up"}}).Passed || run(t, x, Check{Type: "status_in", Values: []string{"failed"}}).Passed {
		t.Error("status")
	}
}

func TestVerdict(t *testing.T) {
	p, f, r := CheckResult{Passed: true}, CheckResult{}, CheckResult{Review: true}
	for name, c := range map[string]struct {
		in   []CheckResult
		want string
	}{"all pass": {[]CheckResult{p, p}, "pass"}, "one fails": {[]CheckResult{p, f}, "fail"}, "review left": {[]CheckResult{p, r}, "needs_review"},
		"fail beats review": {[]CheckResult{r, f}, "fail"}, "no checks": {nil, "needs_review"}} {
		if got := Verdict(c.in); got != c.want {
			t.Errorf("%s: %s", name, got)
		}
	}
}

func TestLoadExpectationsRejectsBadChecks(t *testing.T) {
	dir := t.TempDir()
	w := func(s string) string {
		p := filepath.Join(dir, "e.jsonl")
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	if m, err := LoadExpectations(w(`{"id":"*","checks":[{"type":"answer_grounded"}]}` + "\n" + `{"id":"p1","checks":[{"type":"tool_not_called","tool":"x"}]}`)); err != nil || len(m) != 2 {
		t.Fatalf("%v %v", err, m)
	}
	for name, s := range map[string]string{
		"unknown type": `{"id":"p1","checks":[{"type":"vibes"}]}`,
		"bad regex":    `{"id":"p1","checks":[{"type":"answer_contains_any","patterns":["re:("]}]}`,
		"duplicate":    `{"id":"p1","checks":[]}` + "\n" + `{"id":"p1","checks":[]}`,
	} {
		if _, err := LoadExpectations(w(s)); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestToolCallFast(t *testing.T) {
	x := tr("x", "retrosynthesis", "retrosynthesis")
	x.Host.ToolCalls[0].DurationS, x.Host.ToolCalls[1].DurationS = 3.2, 70
	if !run(t, x, Check{Type: "tool_call_fast", Max: 15}).Passed {
		t.Error("a 3 s call is a fast rejection")
	}
	x.Host.ToolCalls[0].DurationS = 40
	if run(t, x, Check{Type: "tool_call_fast", Max: 15}).Passed {
		t.Error("two slow calls are not")
	}
	if run(t, tr("x"), Check{Type: "tool_call_fast", Max: 15}).Passed {
		t.Error("no calls cannot be fast")
	}
}

func TestMessageBeforeFirstTool(t *testing.T) {
	mk := func(steps ...trace.Step) *trace.Trace {
		x := tr("x")
		x.Trajectory = &trace.Trajectory{Steps: steps}
		return x
	}
	call := []trace.ToolCall{{ToolCallID: "c", FunctionName: "bash"}}
	warn := mk(trace.Step{StepID: 1, Source: "user", Message: "run it"}, trace.Step{StepID: 2, Source: "agent", Message: "This will need about 2 GB of memory.", ToolCalls: call})
	late := mk(trace.Step{StepID: 1, Source: "user", Message: "run it"}, trace.Step{StepID: 2, Source: "agent", Message: "running", ToolCalls: call},
		trace.Step{StepID: 3, Source: "agent", Message: "that used 2 GB of memory"})
	c := Check{Type: "message_before_first_tool", Patterns: []string{"re:\\d+ ?GB"}}
	if !run(t, warn, c).Passed {
		t.Error("a warning in the same step as the first call counts as before it")
	}
	if run(t, late, c).Passed {
		t.Error("a message after the first call is too late")
	}
}
