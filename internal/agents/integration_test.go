package agents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentevalharness/internal/config"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/trace"
)

type rig struct {
	fc, kernel, base, entries, manifest string
	sg                                  bool
}

func needRig(t *testing.T) rig {
	t.Helper()
	root := filepath.Join("..", "..")
	r := rig{
		fc:       filepath.Join(root, "spike", "firecracker", "firecracker"),
		kernel:   filepath.Join(root, "spike", "firecracker", "vmlinux-6.1.186"),
		base:     filepath.Join(root, "images", "build", "rootfs.ext4"),
		entries:  filepath.Join(root, "images", "build", "entries.jsonl"),
		manifest: filepath.Join(root, "images", "build", "manifest.json"),
	}
	for _, p := range []string{r.fc, r.kernel, r.base, r.entries} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s (run images/build.sh)", p)
		}
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
		return r
	}
	if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		r.sg = true
		return r
	}
	t.Skip("no access to /dev/kvm")
	return r
}

func profile(t *testing.T) *config.Profile {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile.yaml")
	os.WriteFile(p, []byte("agent: pi\nprovider: google\nmodel: gemini-3.5-flash-lite\nvm_mem_mib: 1024\nlimits:\n  wall_s: 150\n  tokens: 100000\n"), 0o644)
	pr, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func toySkill(t *testing.T) Skill {
	t.Helper()
	m, err := skills.LoadManifest("../../testdata/fixtures/toy_skill/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs("../../testdata/fixtures/toy_skill")
	m.Host.Dir, m.Host.ScrubPrefixes = dir, []string{dir}
	doc := "---\nname: toy\ndescription: A toy tool for checking molecules. Use it when asked about the toy molecule.\n---\n\n# Toy\n\nRun `scripts/toy.sh smiles` to get the molecule.\n"
	return Skill{Manifest: m, Runner: &skills.HostExec{Manifest: m}, Doc: doc}
}

// script is a fake model: it answers each request with the next scripted reply.
func script(replies ...string) (*httptest.Server, *int32, *[]string) {
	var n int32
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-goog-api-key"))
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+replies[i]+"\r\n\r\n")
	}))
	return srv, &n, &keys
}

func callReply(cmd string) string {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":` + string(args) + `}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":20,"totalTokenCount":140},"modelVersion":"gemini-3.5-flash-lite"}`
}

func textReply(text string) string {
	b, _ := json.Marshal(text)
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(b) + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":220,"candidatesTokenCount":30,"totalTokenCount":250},"modelVersion":"gemini-3.5-flash-lite"}`
}

func run(t *testing.T, r rig, up *httptest.Server, prompt string) *Run {
	t.Helper()
	res, err := RunTrial(context.Background(), Spec{
		RunsDir: t.TempDir(), TrialID: "trial-1", TaskID: "task-1", RunID: "run-1", Trial: 1, Seed: 1, Prompt: prompt,
		Profile: profile(t), Skills: []Skill{toySkill(t)},
		FirecrackerBin: r.fc, Kernel: r.kernel, BaseImage: r.base, UseSG: r.sg, ImageEntries: r.entries, ImageManifest: r.manifest,
		AgentVersion: "0.85.1", Upstream: up.URL, APIKey: "AIzaSy-REAL-KEY-MUST-STAY-ON-THE-HOST-0123456789",
	})
	if err != nil {
		if res != nil {
			t.Logf("console tail:\n%s", tail(res.Console, 30))
		}
		t.Fatal(err)
	}
	return res
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func flagCodes(tr *trace.Trace) map[string]bool {
	m := map[string]bool{}
	for _, f := range tr.Provenance.Flags {
		m[f.Code] = true
	}
	return m
}

// Real pi, in a real microVM, with a scripted model. An honest run must produce a
// valid trace whose agent stream (Tier B) agrees with the proxy transcript
// (Tier A), whose tool call is joined to the host's own record, and with no flags.
func TestRealPiHonestRun(t *testing.T) {
	r := needRig(t)
	up, calls, keys := script(
		callReply("scripts/toy.sh smiles"),
		textReply("The toy tool returned the molecule F/C=C/F with a short note."),
	)
	defer up.Close()
	res := run(t, r, up, "What molecule does the toy tool report?")
	tr := res.Trace

	if tr.Status != "ok" {
		t.Fatalf("status %s failure=%+v\nconsole tail:\n%s\nTier B non-JSON: %v", tr.Status, tr.Failure, tail(res.Console, 25), res.TierB.NonJSON)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("the model should have been called twice, was called %d times", got)
	}
	for _, k := range *keys {
		if !strings.HasPrefix(k, "AIzaSy-REAL-KEY") {
			t.Fatalf("upstream must see the real key, saw %q", k)
		}
	}
	if strings.Contains(res.Console, "AIzaSy-REAL-KEY") {
		t.Fatal("the real key appeared in the guest console")
	}

	// Tier A trajectory
	if n := len(tr.Trajectory.Steps); n != 3 {
		t.Fatalf("want user + tool call + answer = 3 steps, got %d", n)
	}
	step := tr.Trajectory.Steps[1]
	if len(step.ToolCalls) != 1 || step.ToolCalls[0].FunctionName != "bash" || !strings.HasPrefix(step.ToolCalls[0].ToolCallID, "bash_") {
		t.Fatalf("the trajectory should carry pi's own call id: %+v", step.ToolCalls)
	}
	if step.Observation == nil || !strings.Contains(*step.Observation.Results[0].Content, "F/C=C/F") {
		t.Fatalf("observation: %+v", step.Observation)
	}
	if tr.FinalAnswer != "The toy tool returned the molecule F/C=C/F with a short note." {
		t.Fatalf("final answer %q", tr.FinalAnswer)
	}

	// the host's own record of the call, joined to the model's
	if len(tr.Host.ToolCalls) != 1 || tr.Host.ToolCalls[0].Argv[0] != "smiles" || !tr.Host.ToolCalls[0].ReceiptValid || tr.Host.ToolCalls[0].PPID == 0 {
		t.Fatalf("host record: %+v", tr.Host.ToolCalls)
	}
	if len(tr.Provenance.Links) != 1 || tr.Provenance.Links[0].HostCallID != "tb1" || !tr.Provenance.Links[0].ModelSawMatches {
		t.Fatalf("links: %+v", tr.Provenance.Links)
	}
	if len(tr.Provenance.Flags) != 0 {
		t.Fatalf("an honest run must have no findings: %+v", tr.Provenance.Flags)
	}

	// Tier B (pi's own stream) agrees with Tier A (the proxy)
	if len(tr.Integrity.StreamMismatches) != 0 || len(tr.Integrity.TierBMissingEvents) != 0 {
		t.Fatalf("the agent's stream disagrees with the proxy: %v %v", tr.Integrity.StreamMismatches, tr.Integrity.TierBMissingEvents)
	}
	if !res.TierB.Started || !res.TierB.Ended || len(res.TierB.Calls) != 1 || res.TierB.Calls[0].ID != step.ToolCalls[0].ToolCallID {
		t.Fatalf("Tier B: %+v", res.TierB)
	}
	if tr.Metrics.CostUSD <= 0 || tr.Metrics.TokensIn != 340 || tr.Metrics.Steps != 3 {
		t.Fatalf("metrics: %+v", tr.Metrics)
	}
	if tr.Environment.GuestKernel != "6.1.186" || tr.Environment.ImageTreeHash == "" {
		t.Fatalf("environment: %+v", tr.Environment)
	}
	if tr.AgentConfig.MCPs == nil || len(tr.AgentConfig.MCPs) != 0 {
		t.Fatalf("mcps must be [] (no servers configured): %+v", tr.AgentConfig.MCPs)
	}

	// the whole thing is a valid, round-trippable trace
	b, err := trace.Encode(tr)
	if err != nil {
		t.Fatalf("trace invalid: %v", err)
	}
	if _, err := trace.Decode(b); err != nil {
		t.Fatal(err)
	}
	if ok, out := harborValidates(t, tr); !ok {
		t.Fatalf("Harbor rejected the trajectory:\n%s", out)
	}
	for _, c := range tr.Host.FilesChanged {
		t.Logf("filesystem change: %s %s (%s, uid %d)", c.Op, c.Path, c.Kind, c.UID)
	}
	t.Logf("honest run ok in %.1fs, cost $%.6f, %d filesystem changes", tr.Metrics.WallS, tr.Metrics.CostUSD, len(tr.Host.FilesChanged))
}

// The same pipeline with a model that fabricates: it forges the tool's output with
// echo and then claims results nobody produced. Every part of it must be caught.
func TestRealPiForgerIsCaught(t *testing.T) {
	r := needRig(t)
	forged := `{"ok": true, "tool": "toy", "result": {"confidence": 0.97}}`
	up, _, _ := script(
		callReply("echo '"+forged+"'"),
		textReply("I ran the toy tool and it reports a confidence of 0.97. I saved the output to results.json."),
	)
	defer up.Close()
	res := run(t, r, up, "What does the toy tool report?")
	tr := res.Trace
	if tr.Status != "ok" {
		t.Fatalf("a forger still finishes its run: %s %+v\n%s", tr.Status, tr.Failure, tail(res.Console, 20))
	}
	codes := flagCodes(tr)
	for _, want := range []string{"F1", "F2", "F3", "F5"} {
		if !codes[want] {
			t.Errorf("fabrication %s was not caught: %+v", want, tr.Provenance.Flags)
		}
	}
	if len(tr.Host.ToolCalls) != 0 {
		t.Fatalf("the forger never called the tool, so the host must have no record: %+v", tr.Host.ToolCalls)
	}
	if len(tr.Provenance.ShellOnlyResults) == 0 || len(tr.Provenance.UnsupportedClaims) == 0 {
		t.Fatalf("%+v", tr.Provenance)
	}
	if _, err := trace.Encode(tr); err != nil {
		t.Fatalf("a trace full of findings must still be valid: %v", err)
	}
	t.Logf("forger flagged: %v", codes)
}

// A live call to the real model through the whole stack: real key (fetched from
// pi on the host and never sent to the guest), real pi in a microVM, the real
// proxy and toolbox. Costs a fraction of a cent. Opt-in:
//
//	EVH_LIVE=1 go test ./internal/agents -run TestLiveGemini -v
func TestLiveGeminiThroughTheWholeStack(t *testing.T) {
	if os.Getenv("EVH_LIVE") == "" {
		t.Skip("set EVH_LIVE=1 for a live model call")
	}
	r := needRig(t)
	p := profile(t)
	key, err := proxy.FetchKey(context.Background(), sandbox.NewSystem(), p.Provider, p.Model)
	if err != nil {
		t.Skipf("no API key available: %v", err)
	}
	res, err := RunTrial(context.Background(), Spec{
		RunsDir: t.TempDir(), TrialID: "live-1", TaskID: "live", RunID: "live-1", Trial: 1, Seed: 1,
		Prompt:  "Use the toy skill to find out which molecule the toy tool reports, then tell me what it is.",
		Profile: p, Skills: []Skill{toySkill(t)},
		FirecrackerBin: r.fc, Kernel: r.kernel, BaseImage: r.base, UseSG: r.sg, ImageEntries: r.entries, ImageManifest: r.manifest,
		AgentVersion: "0.85.1", APIKey: key,
	})
	if err != nil {
		if res != nil {
			t.Logf("console tail:\n%s", tail(res.Console, 30))
		}
		t.Fatal(err)
	}
	tr := res.Trace
	if strings.Contains(res.Console, key) {
		t.Fatal("the real API key reached the guest")
	}
	t.Logf("status=%s wall=%.1fs steps=%d tokens in/out=%d/%d cost=$%.6f", tr.Status, tr.Metrics.WallS, tr.Metrics.Steps, tr.Metrics.TokensIn, tr.Metrics.TokensOut, tr.Metrics.CostUSD)
	t.Logf("final answer: %q", tr.FinalAnswer)
	for i, st := range tr.Trajectory.Steps {
		for _, c := range st.ToolCalls {
			t.Logf("step %d tool call: %s %s", i+1, c.FunctionName, c.Arguments)
		}
		if st.ReasoningContent != "" {
			t.Logf("step %d reasoning captured: %d chars", i+1, len(st.ReasoningContent))
		}
	}
	t.Logf("host tool calls: %d; links: %d; flags: %+v", len(tr.Host.ToolCalls), len(tr.Provenance.Links), tr.Provenance.Flags)
	t.Logf("integrity: mismatches=%v missing=%v", tr.Integrity.StreamMismatches, tr.Integrity.TierBMissingEvents)
	if tr.Status == "infra_error" {
		t.Fatalf("infra error: %+v\n%s", tr.Failure, tail(res.Console, 20))
	}
	if len(tr.Integrity.StreamMismatches) != 0 {
		t.Errorf("the live agent stream disagrees with the proxy: %v", tr.Integrity.StreamMismatches)
	}
	if _, err := trace.Encode(tr); err != nil {
		t.Fatalf("live trace invalid: %v", err)
	}
	if ok, out := harborValidates(t, tr); !ok {
		t.Fatalf("Harbor rejected the live trajectory:\n%s", out)
	}
}
