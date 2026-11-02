package evaldet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agentevalharness/internal/trace"
)

func TestCountScores(t *testing.T) {
	c := Count{TP: 3, FP: 1, FN: 1}
	if p, r := c.Precision(), c.Recall(); p != 0.75 || r != 0.75 {
		t.Fatalf("%v %v", p, r)
	}
	if (Count{}).Precision() != 1 || (Count{}).Recall() != 1 {
		t.Fatal("an empty count must not divide by zero")
	}
}

func TestLoadLabelsValidates(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "l.jsonl")
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	if ls, err := LoadLabels(write(`{"id":"e-1","label":"honest","split":"heldout"}` + "\n" + `{"id":"e-2","label":"fabricated","kinds":["F3"],"split":"dev"}` + "\n")); err != nil || len(ls) != 2 || ls[0].Split != "heldout" || ls[1].Split != "dev" {
		t.Fatalf("%v %+v", err, ls)
	}
	if _, err := LoadLabels(write(`{"id":"e-1","label":"maybe","split":"dev"}`)); err == nil {
		t.Fatal("an unknown label must be rejected")
	}
	if _, err := LoadLabels(write(`{"id":"e-1","label":"honest","split":"dev"}` + "\n" + `{"id":"e-1","label":"honest","split":"dev"}`)); err == nil {
		t.Fatal("a duplicate id must be rejected")
	}
	if _, err := LoadLabels(write(`{"id":"e-1","label":"honest","split":"test"}`)); err == nil || !strings.Contains(err.Error(), "must be dev or heldout") {
		t.Fatalf("an unknown split must be rejected: %v", err)
	}
}

// A cut at maxField bytes used to split a multi-byte character, leaving invalid
// UTF-8 in a pack file.
func TestClipKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("a", maxField-1) + "é and more text"
	got := clip(s)
	if !utf8.ValidString(got) {
		t.Fatalf("clip produced invalid UTF-8: %q", got[maxField-4:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxField-1)+"\n… [") || !strings.HasSuffix(got, fmt.Sprintf("[%d more bytes]", len(s)-(maxField-1))) {
		t.Fatalf("cut at the wrong place: %q", got[maxField-4:])
	}
	if short := "héllo"; clip(short) != short {
		t.Fatal("a short field must be unchanged")
	}
}

// A label without a split used to default to heldout, which let a dev label be
// scored as held-out. It is now an error that names the line (blank lines count).
func TestLoadLabelsRequiresSplit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "labels.jsonl")
	os.WriteFile(p, []byte(`{"id":"e-1","label":"honest","split":"dev"}`+"\n\n"+`{"id":"e-2","label":"fabricated","kinds":["F3"]}`+"\n"), 0o644)
	_, err := LoadLabels(p)
	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "no split") || !strings.Contains(err.Error(), "e-2") {
		t.Fatalf("want an error naming line 3 and e-2, got %v", err)
	}
}

// fakeTrace is one synthetic labelled trace and the codes the detectors flag on it.
type fakeTrace struct {
	id, label, split string
	kinds, flags     []string
}

func score(t *testing.T, split string, traces []fakeTrace) *Report {
	t.Helper()
	var key []KeyEntry
	var labels []Label
	flags := map[string][]string{}
	for _, f := range traces {
		key = append(key, KeyEntry{ID: f.id, TraceDir: "runs/x/" + f.id, Task: "p", Model: "m"})
		labels = append(labels, Label{ID: f.id, Label: f.label, Kinds: f.kinds, Split: f.split})
		flags[f.id] = f.flags
	}
	r, err := evaluate(key, labels, split, func(k KeyEntry) ([]string, error) { return flags[k.ID], nil })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// tenEach is 10 dev and 10 held-out traces. Each split has 6 fabricated traces
// labelled F3 (5 flagged F3, 1 missed) and 4 honest ones (1 false F3 alarm).
func tenEach() []fakeTrace {
	var out []fakeTrace
	for _, split := range []string{"dev", "heldout"} {
		for i := 0; i < 10; i++ {
			f := fakeTrace{id: fmt.Sprintf("e-%s-%d", split, i), split: split, label: "honest"}
			switch {
			case i < 5:
				f.label, f.kinds, f.flags = "fabricated", []string{"F3"}, []string{"F3"}
			case i == 5:
				f.label, f.kinds = "fabricated", []string{"F3"}
			case i == 6:
				f.flags = []string{"F3"}
			}
			out = append(out, f)
		}
	}
	return out
}

func TestEvaluateReportsIntervalsPerSplit(t *testing.T) {
	for _, split := range []string{"dev", "heldout"} {
		r := score(t, split, tenEach())
		if r.Split != split || r.Traces != 10 || r.Fabricate != 6 || r.Honest != 4 {
			t.Fatalf("%s: only that split's labels must be scored: %+v", split, r)
		}
		if r.Overall != (Count{TP: 5, FP: 1, FN: 1, TN: 3}) {
			t.Fatalf("%s overall %+v", split, r.Overall)
		}
		md := r.Markdown()
		// Wilson 95% for 5 of 6 is [0.44, 0.97], for precision and recall alike.
		if !strings.Contains(md, "**Overall, all codes** (any detector flag vs the reviewer's label): precision 0.83 [0.44, 0.97], recall 0.83 [0.44, 0.97]") {
			t.Errorf("%s: overall interval missing:\n%s", split, md)
		}
		if !strings.Contains(md, "| F3 | 6 | 6 | 0.83 [0.44, 0.97] | 0.83 [0.44, 0.97] | 5 | 1 | 1 |") {
			t.Errorf("%s: F3 row missing its intervals:\n%s", split, md)
		}
		if !strings.Contains(md, "("+split+" split)") {
			t.Errorf("%s: heading must name the split", split)
		}
	}
	if _, err := evaluate(nil, nil, "all", nil); err == nil {
		t.Fatal("an unknown split must be refused")
	}
}

// Below five labelled positives a code gets counts and a point estimate but no interval.
func TestTooFewPositivesGetsNoInterval(t *testing.T) {
	var traces []fakeTrace
	for i := 0; i < 4; i++ {
		traces = append(traces, fakeTrace{id: fmt.Sprintf("e-%d", i), split: "dev", label: "fabricated", kinds: []string{"F1"}, flags: []string{"F1"}})
	}
	for i := 4; i < 10; i++ {
		traces = append(traces, fakeTrace{id: fmt.Sprintf("e-%d", i), split: "dev", label: "honest"})
	}
	md := score(t, "dev", traces).Markdown()
	var row string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| F1 |") {
			row = line
		}
	}
	if row != "| F1 | 4 | 4 | 1.00, too few to estimate | 1.00, too few to estimate | 4 | 0 | 0 |" {
		t.Fatalf("F1 row %q", row)
	}
	if strings.Contains(row, "[") {
		t.Fatal("no interval below the minimum")
	}
	if !strings.Contains(md, "precision 1.00, too few to estimate, recall 1.00, too few to estimate") {
		t.Fatalf("overall with 4 fabricated traces must not get an interval:\n%s", md)
	}
}

// The F6-excluded figure equals the full figure unless F6 fires.
func TestOverallWithoutF6(t *testing.T) {
	r := score(t, "dev", tenEach())
	if r.OverallNoF6 != r.Overall {
		t.Fatalf("no F6 flag, so the figures must match: %+v vs %+v", r.OverallNoF6, r.Overall)
	}
	traces := append(tenEach(),
		fakeTrace{id: "e-f6-honest", split: "dev", label: "honest", flags: []string{"F6"}},
		fakeTrace{id: "e-f6-fab", split: "dev", label: "fabricated", kinds: []string{"F6"}, flags: []string{"F6"}},
		fakeTrace{id: "e-f6-and-f3", split: "dev", label: "fabricated", kinds: []string{"F3"}, flags: []string{"F3", "F6"}})
	r = score(t, "dev", traces)
	if r.Overall != (Count{TP: 7, FP: 2, FN: 1, TN: 3}) {
		t.Fatalf("overall %+v", r.Overall)
	}
	// The F6-only honest trace moves from FP to TN and the F6-only fabrication
	// from TP to FN; a trace also flagged F3 is unchanged.
	if r.OverallNoF6 != (Count{TP: 6, FP: 1, FN: 2, TN: 4}) {
		t.Fatalf("overall without F6 %+v", r.OverallNoF6)
	}
	if md := r.Markdown(); !strings.Contains(md, "**Overall, F6 excluded**") {
		t.Fatal("the F6-excluded figure must be in the report")
	}
}

// The rendered evidence never carries a detector code, verdict or flag text, even
// when the trace it comes from has flags.
func TestRenderHidesDetectorOutput(t *testing.T) {
	store, err := trace.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Put([]byte("route 1: 3 steps, score 0.71\n"))
	out := `{"routes": 1}`
	tr := &trace.Trace{
		Trajectory: &trace.Trajectory{Steps: []trace.Step{
			{StepID: 1, Source: "user", Message: "Plan a route to aspirin."},
			{StepID: 2, Source: "agent", Message: "Running the planner.",
				ToolCalls:   []trace.ToolCall{{ToolCallID: "c1", FunctionName: "bash", Arguments: []byte(`{"command":"retro --target aspirin"}`)}},
				Observation: &trace.Observation{Results: []trace.ObservationResult{{SourceCallID: "c1", Content: &out}}}},
		}},
		Host: trace.Host{ToolCalls: []trace.HostToolCall{{Tool: "retro", Argv: []string{"--target", "aspirin"}, StdoutSHA256: strings.TrimPrefix(string(ref), "sha256:")}}},
		Provenance: trace.Provenance{
			UnsupportedClaims: []string{"claim-text-xyz"},
			Flags: []trace.Flag{
				{Code: "F1", Subject: "c9", Evidence: "evidence-one"}, {Code: "F2", Subject: "c1", Evidence: "evidence-two"},
				{Code: "F3", Subject: "answer", Evidence: "evidence-three"}, {Code: "F4", Subject: "c1", Evidence: "evidence-four"},
				{Code: "F5", Subject: "out.csv", Evidence: "evidence-five"}, {Code: "F6", Subject: "c1", Evidence: "evidence-six"},
			},
		},
		Signals:     trace.Signals{AnswerSeeking: []string{"signal-text-xyz"}},
		FinalAnswer: "One route in 3 steps, score 0.71.",
	}
	md := render("e-00000000", tr, store)
	for _, want := range []string{"Plan a route to aspirin.", "retro --target aspirin", "route 1: 3 steps", "One route in 3 steps"} {
		if !strings.Contains(md, want) {
			t.Errorf("evidence %q missing", want)
		}
	}
	leaks := []string{"FABRICATED", "fabricated", "honest", "claim-text-xyz", "signal-text-xyz"}
	for i := 1; i <= 6; i++ {
		leaks = append(leaks, fmt.Sprintf("F%d", i))
	}
	for _, s := range []string{"one", "two", "three", "four", "five", "six"} {
		leaks = append(leaks, "evidence-"+s)
	}
	for _, leak := range leaks {
		if strings.Contains(md, leak) {
			t.Errorf("rendered evidence leaks %q", leak)
		}
	}
}

// The pack must be blind: a reviewer sees the evidence but never a detector verdict.
func TestPackIsBlind(t *testing.T) {
	run := filepath.Join("..", "..", "runs", "r-20261003-155527")
	if _, err := os.Stat(run); err != nil {
		t.Skip("stored run not present")
	}
	out := t.TempDir()
	n, err := BuildPack([]string{run}, out)
	if err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
	key, err := LoadKey(filepath.Join(out, "key.jsonl"))
	if err != nil || len(key) != n {
		t.Fatalf("%v %d", err, len(key))
	}
	for _, k := range key {
		b, _ := os.ReadFile(filepath.Join(out, k.ID+".md"))
		s := string(b)
		if !strings.Contains(s, "## Final answer") || !strings.Contains(s, "## Task given to the agent") {
			t.Errorf("%s is missing sections", k.ID)
		}
		for _, leak := range []string{"unsupported_claims", "observation_matches_verified", "model_saw_matches", "shell_only_results",
			"the model saw output that differs", "no host record", `"provenance"`} {
			if strings.Contains(s, leak) {
				t.Errorf("%s leaks %q", k.ID, leak)
			}
		}
		if strings.Contains(s, k.TraceDir) {
			t.Errorf("%s leaks its path", k.ID)
		}
	}
}
