package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agentevalharness/internal/config"
	"agentevalharness/internal/guest"
	"agentevalharness/internal/provenance"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/toolproto"
	"agentevalharness/internal/trace"
	"agentevalharness/internal/vm"
)

// Skill is a tool made available to the agent.
type Skill struct {
	Manifest *skills.Manifest
	Runner   skills.ToolRunner
	Doc      string // rendered, leak-checked SKILL.md
}

// Spec describes one trial.
type Spec struct {
	RunsDir, TrialID, TaskID, RunID string
	Trial                           int
	Seed                            int64
	Prompt                          string
	Profile                         *config.Profile
	InputsDir                       string // task input files; "" for none
	Skills                          []Skill

	FirecrackerBin, Kernel, BaseImage string
	UseSG                             bool
	ImageEntries                      string // images/build/entries.jsonl
	ImageManifest                     string // images/build/manifest.json
	AgentVersion                      string

	Upstream string // model API base; "" = Google
	APIKey   string
	VCPUs    int

	// Task toolchain layer (a read-only drive between base and workspace) and the
	// directories it puts first on PATH. Empty for tasks that only need the base image.
	EnvImage, EnvEntries string
	PathPrefix           []string
	// KeepUpper, when set, is an existing directory that receives the agent's
	// final overlay upper layer (KeepUpper/upper) so a verify stage can copy its files.
	KeepUpper string
}

// Run is everything a trial produced.
type Run struct {
	Trace    *trace.Trace
	TierB    TierB
	Console  string // VMM and guest console: infra diagnostics only
	Dir      string
	Exchange []proxy.Exchange
	Records  []skills.CallRecord
	Changes  []sandbox.Change
}

const (
	proxyListen = "127.0.0.1:18080"
	agentUID    = 1000
	agentHome   = "/home/dev"
	piDir       = agentHome + "/.pi-agent"
)

func randKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return []byte(hex.EncodeToString(b))
}

// PiRunSpec builds the supervisor's instructions for a pi run. The prompt goes
// through pi's normal positional input, after "--" so a leading dash is safe.
func PiRunSpec(s Spec) guest.RunSpec {
	p := s.Profile
	argv := []string{"/opt/node/bin/node", "/opt/pi/dist/bundle/cli.js", "--mode", "json",
		"--provider", p.Provider, "--model", p.Model,
		"--no-session", "--offline", "--no-extensions", "--no-context-files", "--no-approve"}
	if len(s.Skills) == 0 {
		argv = append(argv, "--no-skills")
	}
	for _, sk := range s.Skills {
		argv = append(argv, "--skill", path.Join(skills.SkillDocDir, sk.Manifest.Name))
	}
	argv = append(argv, "--", s.Prompt)
	return guest.RunSpec{
		Argv: argv, Dir: agentHome + "/project", UID: agentUID, GID: agentUID, WallS: p.Limits.WallS, IdleS: p.Limits.IdleS, Listen: proxyListen,
		Env: []string{"HOME=" + agentHome, "USER=dev", "LOGNAME=dev", "SHELL=/bin/bash", "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1",
			"PATH=" + GuestPath(s.PathPrefix), "PI_CODING_AGENT_DIR=" + piDir, "PI_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1"},
	}
}

// GuestPath is the agent's PATH, with any task toolchain directories first.
func GuestPath(prefix []string) string {
	return strings.Join(append(append([]string{}, prefix...), "/usr/local/bin", "/usr/bin", "/bin", "/opt/node/bin"), ":")
}

func piModelsJSON() []byte {
	return []byte(`{"providers":{"google":{"baseUrl":"http://` + proxyListen + `/v1beta","api":"google-generative-ai","apiKey":"dummy-key"}}}`)
}

// RunTrial boots a microVM, runs the agent in it, and returns the trace.
func RunTrial(ctx context.Context, s Spec) (*Run, error) {
	if s.Profile == nil || (s.Profile.Agent != "pi" && s.Profile.Agent != "opencode") {
		return nil, fmt.Errorf("the %q adapter does not exist (pi and opencode do)", s.Profile.Agent)
	}
	if s.VCPUs == 0 {
		s.VCPUs = 2
	}
	trialDir, err := sandbox.NewRunDir(s.RunsDir, s.TrialID)
	if err != nil {
		return nil, err
	}
	sys := sandbox.NewSystem()
	run := &Run{Dir: trialDir}

	rec, err := proxy.NewRecorder(filepath.Join(trialDir, "proxy"))
	if err != nil {
		return nil, err
	}
	px, err := proxy.New(proxy.Config{API: s.Profile.API, Upstream: s.Upstream, Key: s.APIKey, Recorder: rec,
		AllowedModels: []string{s.Profile.Model}, TokenBudget: s.Profile.Limits.Tokens})
	if err != nil {
		return nil, err
	}
	toolKey, ctlKey := randKey(), randKey()
	var tools []*skills.Tool
	var manifests []*skills.Manifest
	for _, sk := range s.Skills {
		tools = append(tools, &skills.Tool{Manifest: sk.Manifest, Runner: sk.Runner})
		manifests = append(manifests, sk.Manifest)
	}
	tb, err := skills.NewToolbox(toolKey, tools...)
	if err != nil {
		return nil, err
	}
	ctl := NewControl(ctlKey)

	spec := RunSpecFor(s)
	var persona Persona
	if s.Profile.Realism != "off" {
		persona = NewPersona(s.Seed)
		spec.Hostname = persona.Hostname
	}
	specJSON, _ := json.Marshal(spec)
	root := 0
	extras := []sandbox.ExtraFile{
		{Path: guest.RunSpecPath, Data: specJSON, Mode: 0o400, Owner: &root, ParentMode: "0700"},
		{Path: guest.CtlKeyPath, Data: ctlKey, Mode: 0o400, Owner: &root, ParentMode: "0700"},
		skills.KeyFile(toolKey),
	}
	extras = append(extras, persona.Files...)
	if s.Profile.Agent == "opencode" {
		extras = append(extras, sandbox.ExtraFile{Path: ocConfig, Data: opencodeConfigJSON(s), Mode: 0o644})
	} else {
		extras = append(extras, sandbox.ExtraFile{Path: piDir + "/models.json", Data: piModelsJSON(), Mode: 0o644})
	}
	for _, sk := range s.Skills {
		extras = append(extras, sk.Manifest.Install(sk.Doc)...)
	}
	inputs := s.InputsDir
	if inputs == "" {
		if inputs, err = os.MkdirTemp("", "evh-inputs-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(inputs)
	}
	wsImg := filepath.Join(trialDir, "workspace.ext4")
	wsEntries, err := sandbox.BuildWorkspaceWith(ctx, sys, inputs, wsImg, sandbox.DefaultIdentities, extras)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	baseEntries, err := sandbox.LoadEntries(s.ImageEntries)
	if err != nil {
		return nil, fmt.Errorf("image entries: %w", err)
	}

	var layers [][]sandbox.Entry
	layers = append(layers, baseEntries)
	if s.EnvImage != "" {
		envEntries, err := sandbox.LoadEntries(s.EnvEntries)
		if err != nil {
			return nil, fmt.Errorf("env entries: %w", err)
		}
		layers = append(layers, envEntries)
	}
	layers = append(layers, wsEntries)

	cfg := vm.Config{FirecrackerBin: s.FirecrackerBin, Kernel: s.Kernel, BaseImage: s.BaseImage, Init: "/guest-init",
		RunDir: filepath.Join(trialDir, "vm"), VCPUs: s.VCPUs, MemMiB: s.Profile.VMMemMiB, ScratchMiB: 256,
		WorkspaceImage: wsImg, EnvImage: s.EnvImage, WallS: s.Profile.Limits.WallS + 60, UseSG: s.UseSG}
	v, err := vm.New(cfg)
	if err != nil {
		return nil, err
	}
	defer v.Destroy()
	for port, h := range map[int]vm.Handler{5000: px.ServeConn, toolPort: tb.ServeConn, guest.ControlPort: ctl.ServeConn} {
		if err := v.Listen(port, h); err != nil {
			return nil, err
		}
	}
	tb.Epoch = time.Now()
	if err := v.Start(ctx); err != nil {
		return nil, err
	}
	res, werr := v.Wait(ctx)
	run.Console = v.Console()
	if werr != nil {
		return run, werr
	}

	// evidence, collected before the VM's directory is removed
	run.Exchange = rec.Snapshot()
	run.Records = tb.Records()
	_, rejected := tb.Rejections()
	lower := sandbox.NewLower(layers...)
	scratch := filepath.Join(cfg.RunDir, "scratch.img")
	var diffErr, scratchErr error
	if scratchErr = sandbox.CheckScratch(ctx, sys, scratch); scratchErr == nil || errorsIsFull(scratchErr) {
		run.Changes, diffErr = sandbox.ExtractDiffKeep(ctx, sys, scratch, lower, sandbox.DefaultIdentities, s.KeepUpper)
	}
	if err := rec.Flush(); err != nil {
		return run, err
	}
	run.TierB = ParseEvents(s.Profile.Agent, ctl.Lines(guest.StreamStdout))

	tr, err := assemble(s, run, res, ctl, px, rejected, manifests, diffErr, scratchErr)
	if err != nil {
		return run, err
	}
	run.Trace = tr
	return run, nil
}

const toolPort = 5001

var _ = toolproto.Version

func errorsIsFull(err error) bool {
	return err != nil && strings.Contains(err.Error(), "scratch disk is full")
}

func assemble(s Spec, run *Run, res sandbox.Result, ctl *Control, px *proxy.Proxy, rejected int,
	manifests []*skills.Manifest, diffErr, scratchErr error) (*trace.Trace, error) {
	p := s.Profile
	tr := &trace.Trace{TaskID: s.TaskID, RunID: s.RunID, Trial: s.Trial, Seed: s.Seed}
	var skillNames []string
	for _, m := range manifests {
		skillNames = append(skillNames, m.Name)
	}
	tr.AgentConfig = trace.AgentConfig{Agent: p.Agent, Version: s.AgentVersion, Provider: p.Provider, Model: p.Model, Skills: skillNames,
		MCPs: []string{}, Web: false, ToolMode: p.ToolMode, SkillRunner: p.SkillRunner, Realism: p.Realism}
	tr.Environment = environment(s)

	traj, err := trace.FromProxy(run.Exchange, s.Prompt, trace.ATIFAgent{Name: p.Agent, Version: s.AgentVersion, ModelName: p.Model})
	if err != nil {
		return nil, fmt.Errorf("trajectory: %w", err)
	}
	tr.Trajectory = traj
	tr.FinalAnswer = finalAnswer(run.Exchange)

	store, err := trace.NewStore(filepath.Join(run.Dir, "blobs"))
	if err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(run.Dir, "proxy", "exchanges.jsonl")); err == nil {
		tr.Host.ProxyTranscript, _ = store.Put(b)
	}
	for _, r := range run.Records {
		tr.Host.ToolCalls = append(tr.Host.ToolCalls, r.ToTrace())
		_, _ = store.Put(r.Result.Stdout) // content-addressed: blobs/<stdout_sha256>
		_, _ = store.Put(r.Result.Stderr)
	}
	if len(run.Records) > 0 {
		if b, err := json.Marshal(run.Records); err == nil {
			tr.Host.CallRecords, _ = store.Put(b)
		}
	}
	tr.Host.FilesChanged = run.Changes

	var docs []string
	for _, sk := range s.Skills {
		docs = append(docs, sk.Doc)
	}
	f := provenance.Analyze(provenance.Input{Prompt: s.Prompt, Exchanges: run.Exchange, Records: run.Records, Rejections: rejected,
		Changes: run.Changes, FinalAnswer: tr.FinalAnswer, Manifests: manifests, Docs: docs})
	f.Apply(tr)
	if n := ctl.Rejected(); n > 0 {
		tr.Provenance.Flags = append(tr.Provenance.Flags, trace.Flag{Code: "U2", Subject: "guest",
			Evidence: fmt.Sprintf("%d control frame(s) failed verification: something in the guest wrote to the supervisor's channel without the key", n)})
	}

	mism, missing := CrossCheck(run.TierB, run.Exchange)
	tr.Integrity.StreamMismatches = mism
	tr.Integrity.TierBMissingEvents = missing
	if n := len(run.TierB.NonJSON); n > 0 {
		tr.Integrity.StreamMismatches = append(tr.Integrity.StreamMismatches, fmt.Sprintf("%d line(s) of the agent's stdout were not JSON events", n))
	}
	if ctl.Capped() {
		tr.Integrity.StreamMismatches = append(tr.Integrity.StreamMismatches, "the guest exceeded the control channel's size limits")
	}
	for _, c := range run.Changes {
		if c.UID == 0 || c.UID == skills.ToolUID {
			tr.Integrity.UnexplainedFileChanges = append(tr.Integrity.UnexplainedFileChanges, fmt.Sprintf("%s change by uid %d: %s", c.Op, c.UID, c.Path))
		}
	}

	tr.Metrics = trace.Metrics{WallS: res.WallS, Steps: len(traj.Steps), CostUSD: run.TierB.CostUSD,
		AgentRetries: run.TierB.Retries, AgentCompactions: run.TierB.Compactions}
	if traj.FinalMetrics != nil {
		if traj.FinalMetrics.TotalPromptTokens != nil {
			tr.Metrics.TokensIn = *traj.FinalMetrics.TotalPromptTokens
		}
		if traj.FinalMetrics.TotalCompletionTokens != nil {
			tr.Metrics.TokensOut = *traj.FinalMetrics.TotalCompletionTokens
		}
	}
	classify(tr, run, res, ctl, px, diffErr, scratchErr)
	return tr, nil
}

// classify sets status and attribution. A fault of the harness is infra and is
// excluded from agent scores; what the agent did to itself is not.
func classify(tr *trace.Trace, run *Run, res sandbox.Result, ctl *Control, px *proxy.Proxy, diffErr, scratchErr error) {
	infra := func(stage, reason, cause string) {
		tr.Status = "infra_error"
		tr.Failure = &trace.Failure{Stage: stage, Reason: reason, Cause: cause, Attribution: "infra"}
	}
	switch {
	case res.Exit == vm.ExitGuestPanic || res.Exit == vm.ExitVMMCrash:
		infra("boot", res.Exit, "the microVM did not run cleanly")
	case res.Exit == vm.ExitTimeout:
		tr.Status = "timeout"
		tr.Failure = &trace.Failure{Stage: "run", Reason: "timeout", Cause: "the VM exceeded its wall-clock limit", Attribution: "agent"}
	case toolOOM(run.Records):
		infra("tool", "tool_oom", "the tool was killed by the kernel, most likely for lack of memory on the host; the result says nothing about the agent")
	case scratchErr != nil && errorsIsFull(scratchErr):
		infra("filesystem", "scratch_full", "the guest ran out of writable space, so the file diff may be incomplete")
	case diffErr != nil:
		infra("collect", "diff_failed", diffErr.Error())
	case ctl.Exit() == nil:
		infra("run", "no_exit_frame", "the guest never reported an exit")
	case hasInfra(run.Exchange) && tr.FinalAnswer == "":
		infra("model", "upstream", "the model API failed (rate limit or outage) and no answer was produced")
	case px.BudgetExceeded():
		tr.Status = "budget_exceeded"
		tr.Failure = &trace.Failure{Stage: "model", Reason: "budget_exceeded", Cause: "the token budget was exhausted", Attribution: "agent"}
	case ctl.Exit().Reason == "timeout":
		tr.Status = "timeout"
		tr.Failure = &trace.Failure{Stage: "run", Reason: "timeout", Cause: "the agent exceeded its wall-clock limit", Attribution: "agent"}
	case ctl.Exit().Reason == "idle" && px != nil && px.InFlight() > 0:
		// The agent was waiting on a model request that never completed. The stall is
		// the model API's, so the agent is not scored for it.
		infra("model", "upstream_hang", "the run was ended while a model request had not completed; the model API stalled")
	case ctl.Exit().Reason == "idle":
		tr.Status = "timeout"
		tr.Failure = &trace.Failure{Stage: "run", Reason: "idle", Cause: "the agent produced no output for too long and was ended", Attribution: "agent"}
	case ctl.Exit().Code != 0 && tr.FinalAnswer == "":
		tr.Status = "failed"
		tr.Failure = &trace.Failure{Stage: "run", Reason: "agent_exit", Cause: fmt.Sprintf("the agent exited with code %d and gave no answer", ctl.Exit().Code), Attribution: "agent"}
	case tr.FinalAnswer == "":
		tr.Status = "gave_up"
	default:
		tr.Status = "ok"
	}
}

// toolOOM reports a tool killed by a signal the harness did not send. When every
// call to a tool ends that way the agent never had a working tool.
func toolOOM(recs []skills.CallRecord) bool {
	for _, r := range recs {
		if r.Result.OOMSuspect {
			return true
		}
	}
	return false
}

func hasInfra(ex []proxy.Exchange) bool {
	for _, e := range ex {
		if e.Infra != "" {
			return true
		}
	}
	return false
}

func finalAnswer(ex []proxy.Exchange) string {
	for i := len(ex) - 1; i >= 0; i-- {
		if ex[i].Status == 200 && ex[i].Rejected == "" && len(ex[i].ToolCalls) == 0 && ex[i].Text != "" {
			return ex[i].Text
		}
	}
	return ""
}

func environment(s Spec) trace.Environment {
	env := trace.Environment{Firecracker: sandbox.PinnedFirecrackerVersion, MemPeakMethod: "getrusage maxrss (tool); memory.peak (guest)"}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		env.HostKernel = strings.TrimSpace(string(b))
	}
	env.GuestKernel = strings.TrimPrefix(filepath.Base(s.Kernel), "vmlinux-")
	if b, err := os.ReadFile(s.ImageManifest); err == nil {
		var m struct {
			TreeHash string `json:"tree_hash"`
		}
		if json.Unmarshal(b, &m) == nil {
			env.ImageTreeHash = m.TreeHash
		}
	}
	return env
}
