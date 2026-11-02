package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentevalharness/internal/proxy"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/trace"
)

const stdout = `{"ok": true, "tool": "retrosynthesis", "mode": "run_skill", "result": {"is_feasible": true, "best_route_stats": {"num_steps": 1, "availability_ratio": 1.0, "confidence": 0.58}}, "error": null}` + "\n"

// storedTrial writes a trial directory the way RunTrial leaves one.
func storedTrial(t *testing.T, answer string, oldFlags []trace.Flag) string {
	t.Helper()
	dir := t.TempDir()
	store, err := trace.NewStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": "scripts/run_retrosynthesis.sh --list-skills"})
	ex := []proxy.Exchange{
		{Index: 0, Status: 200, ToolCalls: []proxy.ToolCall{{ID: "g0.0.aa", AgentID: "call_1", Name: "bash", Args: args}}},
		{Index: 1, Status: 200, ToolResults: []proxy.ToolResult{{CallID: "g0.0.aa", AgentCallID: "call_1", Name: "bash", Content: mustJSON(map[string]string{"output": stdout})}}},
		{Index: 2, Status: 200, Text: answer},
	}
	var tl []byte
	for _, e := range ex {
		b, _ := json.Marshal(e)
		tl = append(tl, append(b, '\n')...)
	}
	tref, _ := store.Put(tl)
	rec := []skills.CallRecord{{CallID: "tb1", Tool: "retrosynthesis", Argv: []string{"--list-skills"}, ArgsValid: true,
		Params: map[string]any{"mode": "list_skills"}, Result: skills.Result{Stdout: []byte(stdout), ExitCode: 0}}}
	rb, _ := json.Marshal(rec)
	rref, _ := store.Put(rb)

	traj, err := trace.FromProxy(ex, "Is aspirin synthesizable?", trace.ATIFAgent{Name: "pi", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	tr := &trace.Trace{TaskID: "t", RunID: "r", Status: "ok", FinalAnswer: answer, Trajectory: traj,
		AgentConfig: trace.AgentConfig{Agent: "pi", Version: "1", Provider: "google", Model: "m", Skills: []string{"retrosynthesis"}, MCPs: []string{}}}
	tr.Host.ProxyTranscript, tr.Host.CallRecords = tref, rref
	tr.Host.ToolCalls = []trace.HostToolCall{rec[0].ToTrace()}
	tr.Provenance.Flags = oldFlags
	enc, err := trace.Encode(tr)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "trace.json"), enc, 0o644)
	return dir
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestReanalyzeDropsAFalsePositiveAndKeepsTheOriginal(t *testing.T) {
	// the stored flag is one an old detector wrongly raised on an honest answer
	dir := storedTrial(t, "It is feasible: 1 step and a confidence of 0.58.", []trace.Flag{{Code: "F6", Subject: "x", Evidence: "old detector"}, {Code: "U1", Subject: "guest", Evidence: "1 call(s) reached the toolbox without a valid signature"}})
	before, _ := os.ReadFile(filepath.Join(dir, "trace.json"))
	tr, err := Reanalyze("../..", dir)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, f := range tr.Provenance.Flags {
		codes = append(codes, f.Code)
	}
	if strings.Join(codes, ",") != "U1" {
		t.Fatalf("F6 was a false positive and should be gone; U1 cannot be recomputed and must be carried over: %v", codes)
	}
	if len(tr.Provenance.Links) != 1 || tr.Provenance.Links[0].HostCallID != "tb1" {
		t.Fatalf("the re-analysis should rebuild the links: %+v", tr.Provenance.Links)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "trace.json"))
	if string(before) != string(after) {
		t.Fatal("re-analysis must never modify the original trace")
	}
}

func TestReanalyzeFindsWhatAnOldDetectorMissed(t *testing.T) {
	dir := storedTrial(t, "Feasible, and the route score is 0.91.", nil)
	tr, err := Reanalyze("../..", dir)
	if err != nil {
		t.Fatal(err)
	}
	var f3 bool
	for _, f := range tr.Provenance.Flags {
		f3 = f3 || f.Code == "F3"
	}
	if !f3 {
		t.Fatalf("0.91 is in no verified output: %+v", tr.Provenance.Flags)
	}
}

func TestReanalyzeRunWritesBesideTheOriginals(t *testing.T) {
	runDir := t.TempDir()
	d := storedTrial(t, "Feasible.", nil)
	os.MkdirAll(filepath.Join(runDir, "task"), 0o755)
	if err := os.Rename(d, filepath.Join(runDir, "task", "trial-1")); err != nil {
		t.Fatal(err)
	}
	res, err := ReanalyzeRun("../..", runDir)
	if err != nil || len(res) != 1 || res[0].Error != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "task", "trial-1", "trace.reanalyzed.json")); err != nil {
		t.Fatal(err)
	}
}

func TestOldRunsWithoutCallRecordsAreSkippedClearly(t *testing.T) {
	dir := storedTrial(t, "Feasible.", nil)
	raw, _ := os.ReadFile(filepath.Join(dir, "trace.json"))
	var m map[string]any
	json.Unmarshal(raw, &m)
	delete(m["host"].(map[string]any), "call_records") // a run from before records were stored
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "trace.json"), b, 0o644)
	if _, err := Reanalyze("../..", dir); err == nil || !strings.Contains(err.Error(), "predates stored call records") {
		t.Fatalf("an old run must say why it cannot be re-analysed: %v", err)
	}
}
