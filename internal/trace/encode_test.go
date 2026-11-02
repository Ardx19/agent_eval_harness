package trace

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentevalharness/internal/proxy"
	"agentevalharness/internal/sandbox"
)

func ip(i int) *int       { return &i }
func sp(s string) *string { return &s }

func goldenTrace() *Trace {
	call := ToolCall{ToolCallID: "tc1", FunctionName: "bash", Arguments: json.RawMessage(`{"cmd":"retrosynthesis.out --smiles CCO"}`)}
	return &Trace{
		TaskID: "9f2c41aa", RunID: "run-0001", Trial: 3, Seed: 1,
		AgentConfig: AgentConfig{Agent: "pi", Version: "0.85.1", Provider: "google", Model: "gemini-3.5-flash-lite",
			Skills: []string{"retrosynthesis"}, MCPs: []string{}, ToolMode: "cold", SkillRunner: "stub", Realism: "default"},
		Environment: Environment{HostKernel: "5.15.167.4-microsoft-standard-WSL2", Firecracker: "v1.17.0", GuestKernel: "6.1.186",
			ImageTreeHash: "887a5622eca6140cd09ff812096779be82c02184e6f522a9b449796ed258ddc7", MemPeakMethod: "memory.peak"},
		Status: "ok",
		Trajectory: &Trajectory{
			SchemaVersion: ATIFVersion,
			Agent:         ATIFAgent{Name: "pi", Version: "0.85.1", ModelName: "gemini-3.5-flash-lite"},
			Steps: []Step{
				{StepID: 1, Source: "user", Message: "Is ethanol synthesizable?", Timestamp: "2026-10-03T10:00:00Z"},
				{StepID: 2, Source: "agent", Timestamp: "2026-10-03T10:00:03Z", ModelName: "gemini-3.5-flash-lite",
					Message: "I'll check.", ReasoningContent: "need the retrosynthesis skill",
					ToolCalls:   []ToolCall{call},
					Observation: &Observation{Results: []ObservationResult{{SourceCallID: "tc1", Content: sp(`{"ok":true}`)}}},
					Metrics:     &StepMetrics{PromptTokens: ip(120), CompletionTokens: ip(30)}},
				{StepID: 3, Source: "agent", Timestamp: "2026-10-03T10:00:09Z", Message: "Yes, it is feasible.",
					Metrics: &StepMetrics{PromptTokens: ip(180), CompletionTokens: ip(12)}},
			},
			FinalMetrics: &FinalMetrics{TotalPromptTokens: ip(300), TotalCompletionTokens: ip(42), TotalSteps: ip(3)},
		},
		Host: Host{
			ToolCalls: []HostToolCall{{CallID: "tb1", PID: 4123, Tool: "retrosynthesis", Argv: []string{"--smiles", "CCO"},
				ArgsValid: true, TStart: 3.1, DurationS: 2.6, StdoutSHA256: strings.Repeat("a", 64), ReceiptValid: true,
				CPUS: 2.1, PeakMemMB: 812, Outputs: []Output{{Path: "out/route.json", SHA256: strings.Repeat("b", 64), Bytes: 920}}}},
			ProxyTranscript: Ref("sha256:" + strings.Repeat("c", 64)),
			FilesChanged:    []sandbox.Change{{Path: "/home/dev/project/out/route.json", Op: "create", Kind: "file", Perm: 0o644, UID: 2000, GID: 2000, Size: 920, SHA256: strings.Repeat("b", 64), ContentChanged: true, WriterHint: "tool"}},
		},
		Provenance:  Provenance{Links: []Link{{ToolCallID: "tc1", ProxyToolUseID: "g0.0.ab12cd34", HostCallID: "tb1", ObservationMatchesVerified: true, ModelSawMatches: true}}},
		FinalAnswer: "Yes, it is feasible.",
		Metrics:     Metrics{WallS: 11.2, Steps: 3, TokensIn: 300, TokensOut: 42},
	}
}

func TestGoldenAndRoundTrip(t *testing.T) {
	b, err := Encode(goldenTrace())
	if err != nil {
		t.Fatal(err)
	}
	golden := "../../testdata/fixtures/trace_golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.WriteFile(golden, b, 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("encoding changed; review the diff and rerun with UPDATE_GOLDEN=1 if intended\n--- got ---\n%s", b)
	}
	dec, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(dec)
	if err != nil || !bytes.Equal(again, b) {
		t.Fatalf("decode -> encode must be byte-identical: %v", err)
	}
	if !strings.Contains(string(b), `"mcps": []`) {
		t.Fatal(`mcps must be present as [] (it means "no servers configured")`)
	}
	// the only null is injection_followed: null = "not an injection task"
	if n := strings.Count(string(b), "null"); n != 1 || !strings.Contains(string(b), `"injection_followed": null`) {
		t.Fatalf("absent data must be [] and never null, except injection_followed (found %d nulls)", n)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, _ := Encode(goldenTrace())
	b, _ := Encode(goldenTrace())
	if !bytes.Equal(a, b) {
		t.Fatal("same input must give byte-identical output")
	}
}

func TestValidateRules(t *testing.T) {
	cases := map[string]func(*Trace){
		"unknown status":         func(t *Trace) { t.Status = "sort_of_ok" },
		"ok with a failure":      func(t *Trace) { t.Failure = &Failure{Attribution: "agent"} },
		"failed without failure": func(t *Trace) { t.Status = "failed" },
		"bad attribution":        func(t *Trace) { t.Status = "failed"; t.Failure = &Failure{Attribution: "gremlins"} },
		"infra not attributed":   func(t *Trace) { t.Status = "infra_error"; t.Failure = &Failure{Attribution: "agent"} },
		"no task id":             func(t *Trace) { t.TaskID = "" },
		"mcps absent":            func(t *Trace) { t.AgentConfig.MCPs = nil },
		"broken trajectory":      func(t *Trace) { t.Trajectory.Steps[1].StepID = 9 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			tr := goldenTrace()
			tr.Normalize()
			mut(tr)
			if name == "mcps absent" {
				tr.AgentConfig.MCPs = nil
			}
			if err := tr.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
	ok := goldenTrace()
	ok.Status, ok.Failure = "infra_error", &Failure{Stage: "boot", Reason: "guest_panic", Cause: "kernel panic", Attribution: "infra"}
	ok.Normalize()
	if err := ok.Validate(); err != nil {
		t.Fatalf("a properly attributed infra_error is valid: %v", err)
	}
}

func TestDecodeIsStrict(t *testing.T) {
	b, _ := Encode(goldenTrace())
	bad := strings.Replace(string(b), `"trial": 3,`, `"trial": 3, "surprise": true,`, 1)
	if _, err := Decode([]byte(bad)); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	nullMCPs := strings.Replace(string(b), `"mcps": []`, `"mcps": null`, 1)
	if _, err := Decode([]byte(nullMCPs)); err == nil {
		t.Fatal(`"mcps": null must be rejected: it hides whether MCP servers were configured`)
	}
}

func TestBlobStore(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("exec-log-line\n"), 200000)
	r1, err := s.Put(big)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Put(big)
	if r1 != r2 || !r1.Valid() {
		t.Fatalf("identical content must share one blob: %s %s", r1, r2)
	}
	if got, err := s.Get(r1); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("round trip: %v", err)
	}
	// a body is referenced, never inlined: the trace stays small and readable
	tr := goldenTrace()
	tr.Host.ExecLog = r1
	enc, _ := Encode(tr)
	if len(enc) > 8192 || bytes.Contains(enc, []byte("exec-log-line")) {
		t.Fatalf("large bodies must be referenced, not inlined (%d bytes)", len(enc))
	}
	// tampering after the fact is detected
	p := filepath.Join(s.Dir, strings.TrimPrefix(string(r1), "sha256:"))
	os.WriteFile(p, []byte("forged"), 0o644)
	if _, err := s.Get(r1); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("a modified blob must be reported: %v", err)
	}
	if _, err := s.Get(Ref("sha256:zz")); err == nil {
		t.Fatal("malformed refs must be rejected")
	}
}

func TestATIFRules(t *testing.T) {
	for name, mut := range atifMutations() {
		t.Run(name, func(t *testing.T) {
			tr := goldenTrace().Trajectory
			mut(tr)
			if err := ValidateATIF(tr); err == nil {
				t.Fatal("expected an ATIF violation")
			}
		})
	}
	if err := ValidateATIF(goldenTrace().Trajectory); err != nil {
		t.Fatalf("golden trajectory must be valid: %v", err)
	}
}

func atifMutations() map[string]func(*Trajectory) {
	return map[string]func(*Trajectory){
		"reasoning on a user step":  func(t *Trajectory) { t.Steps[0].ReasoningContent = "x" },
		"tool_calls on a user step": func(t *Trajectory) { t.Steps[0].ToolCalls = t.Steps[1].ToolCalls },
		"step id gap":               func(t *Trajectory) { t.Steps[2].StepID = 5 },
		"unknown source":            func(t *Trajectory) { t.Steps[1].Source = "assistant" },
		"dangling source_call_id":   func(t *Trajectory) { t.Steps[1].Observation.Results[0].SourceCallID = "nope" },
		"arguments not an object":   func(t *Trajectory) { t.Steps[1].ToolCalls[0].Arguments = json.RawMessage(`"ls"`) },
		"missing agent version":     func(t *Trajectory) { t.Agent.Version = "" },
		"bad timestamp":             func(t *Trajectory) { t.Steps[0].Timestamp = "yesterday" },
		"metrics on a user step":    func(t *Trajectory) { t.Steps[0].Metrics = t.Steps[1].Metrics },
		"duplicate tool_call_id": func(t *Trajectory) {
			t.Steps[1].ToolCalls = append(t.Steps[1].ToolCalls, t.Steps[1].ToolCalls[0])
		},
	}
}

func TestFromProxy(t *testing.T) {
	start := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	ex := []proxy.Exchange{
		{Index: 0, Start: start, End: start.Add(2 * time.Second), Status: 200, Model: "gemini-3.5-flash-lite",
			Thinking: []string{"need the skill"}, Text: "checking",
			ToolCalls: []proxy.ToolCall{{ID: "g0.0.aa", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}},
			Usage:     proxy.Usage{Prompt: 100, Completion: 10, Thoughts: 5, Total: 115}},
		{Index: 1, Start: start.Add(3 * time.Second), End: start.Add(4 * time.Second), Status: 200, Text: "two files",
			ToolResults: []proxy.ToolResult{{CallID: "g0.0.aa", Name: "bash", Content: `{"output":"a b"}`}},
			Usage:       proxy.Usage{Prompt: 140, Completion: 4, Total: 144}},
		{Index: 2, Status: 403, Rejected: "server_tool:googleSearch"}, // not a model step
		{Index: 3, Status: 429, Infra: "rate_limited"},                // not a model step
	}
	tr, err := FromProxy(ex, "list the files", ATIFAgent{Name: "pi", Version: "0.85.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) != 3 || tr.Steps[0].Source != "user" || tr.Steps[0].Message != "list the files" {
		t.Fatalf("steps: %+v", tr.Steps)
	}
	a := tr.Steps[1]
	if a.ReasoningContent != "need the skill" || len(a.ToolCalls) != 1 || a.Observation == nil ||
		a.Observation.Results[0].SourceCallID != "g0.0.aa" || *a.Observation.Results[0].Content != `{"output":"a b"}` {
		t.Fatalf("tool call must carry its observation as the model saw it: %+v", a)
	}
	if *a.Metrics.CompletionTokens != 15 {
		t.Fatalf("thought tokens count as output: %+v", a.Metrics)
	}
	if *tr.FinalMetrics.TotalPromptTokens != 240 || *tr.FinalMetrics.TotalSteps != 3 {
		t.Fatalf("final metrics: %+v", tr.FinalMetrics)
	}
	// a result nobody asked for stays visible instead of being dropped
	ex = append(ex, proxy.Exchange{Index: 4, Status: 200, Text: "ok", ToolResults: []proxy.ToolResult{{Name: "retrosynthesis", Content: `{"x":1}`, Unmatched: true}}})
	tr, err = FromProxy(ex, "p", ATIFAgent{Name: "pi", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Extra["unmatched_tool_results"]; !ok {
		t.Fatalf("unmatched results must be preserved: %+v", tr.Extra)
	}
}

// --- differential test against Harbor's own validator ---

const harborPython = "../../tools/harbor-venv/bin/python"

func harborAccepts(t *testing.T, tr *Trajectory) (bool, string) {
	t.Helper()
	b, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "trajectory.json")
	os.WriteFile(f, b, 0o644)
	out, err := exec.Command(harborPython, "-m", "harbor.utils.trajectory_validator", "--no-validate-images", f).CombinedOutput()
	return err == nil, string(out)
}

func needHarbor(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(harborPython); err != nil {
		t.Skip("Harbor validator not installed (run tools/setup-harbor.sh)")
	}
}

func TestHarborAgreesOnValidTrajectories(t *testing.T) {
	needHarbor(t)
	if ok, out := harborAccepts(t, goldenTrace().Trajectory); !ok {
		t.Fatalf("Harbor rejected our golden trajectory:\n%s", out)
	}
	start := time.Now()
	tr, err := FromProxy([]proxy.Exchange{{Index: 0, Start: start, End: start, Status: 200, Text: "hi", Usage: proxy.Usage{Prompt: 3, Completion: 1, Total: 4}}},
		"hello", ATIFAgent{Name: "pi", Version: "0.85.1", ModelName: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, out := harborAccepts(t, tr); !ok {
		t.Fatalf("Harbor rejected a FromProxy trajectory:\n%s", out)
	}
}

// Where we are deliberately stricter than Harbor 0.23.0's validator. The RFC
// requires agent.version, and a duplicate tool_call_id would make the
// provenance join ambiguous, yet the validator accepts both.
var stricterThanHarbor = map[string]string{
	"missing agent version":  "RFC requires agent.version; Harbor's validator does not enforce it",
	"duplicate tool_call_id": "ambiguous ids would break the provenance join",
}

func TestHarborAgreesOnViolations(t *testing.T) {
	needHarbor(t)
	for name, mut := range atifMutations() {
		t.Run(name, func(t *testing.T) {
			tr := goldenTrace().Trajectory
			mut(tr)
			goErr := ValidateATIF(tr)
			ok, out := harborAccepts(t, tr)
			if why, intended := stricterThanHarbor[name]; intended {
				if goErr == nil {
					t.Errorf("we must reject this (%s)", why)
				}
				if !ok {
					t.Logf("Harbor now rejects this too; the divergence entry can be removed (%s)", why)
				}
				return
			}
			switch {
			case goErr != nil && ok:
				t.Errorf("we reject but Harbor accepts (our rule is stricter than the spec): %v", goErr)
			case goErr == nil && !ok:
				t.Errorf("Harbor rejects but we accept (we would emit invalid ATIF):\n%s", out)
			}
		})
	}
}
