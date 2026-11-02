// Package runner turns a prompts file into traces: one microVM trial per task per
// repeat, each written as a trace.json, under a session budget.
package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"agentevalharness/internal/agents"
	"agentevalharness/internal/config"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/trace"
	"agentevalharness/internal/verify"
)

// Task is one line of the prompts file. The runner never reads expectations.
type Task struct {
	ID     string   `json:"id"`
	Prompt string   `json:"prompt"`
	Inputs []string `json:"inputs"`
	Limits *struct {
		WallS  int `json:"wall_s"`
		Tokens int `json:"tokens"`
		MemMB  int `json:"mem_mb"`
	} `json:"limits"`
	Tags []string `json:"tags"`

	// Coding tasks: Env names a prebuilt toolchain and checkout under envs/build/<Env>/
	// (repo/ is the agent's workspace, env.ext4 its toolchain layer); Verify is the
	// host-only directory (relative to the prompts file) holding the hidden tests.
	Env    string `json:"env,omitempty"`
	Verify string `json:"verify,omitempty"`
	// Workspace is a directory (relative to the prompts file) whose contents become
	// /home/dev/project. It replaces the env's repo/ and lets a task have no repository
	// at all; Env may then name just a toolchain layer (git, jq, sqlite3).
	Workspace string `json:"workspace,omitempty"`
}

// taskDirs resolves where a coding task's pristine workspace and toolchain layer are.
func taskDirs(root, baseDir string, task Task) (repoDir, envImage, envEntries string, err error) {
	if task.Workspace != "" {
		repoDir = filepath.Join(baseDir, task.Workspace)
	}
	if task.Env != "" {
		envDir := filepath.Join(root, "envs", "build", task.Env)
		envImage, envEntries = filepath.Join(envDir, "env.ext4"), filepath.Join(envDir, "env.ext4.entries.jsonl")
		if repoDir == "" {
			repoDir = filepath.Join(envDir, "repo")
		}
	}
	for _, p := range []string{envImage, envEntries, repoDir} {
		if p == "" {
			continue
		}
		if _, e := os.Stat(p); e != nil {
			return "", "", "", fmt.Errorf("task environment is not built: %w", e)
		}
	}
	return repoDir, envImage, envEntries, nil
}

var reID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// LoadTasks reads a JSONL prompts file, rejecting duplicate or unusable ids.
func LoadTasks(path string) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tasks []Task
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var t Task
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		switch {
		case !reID.MatchString(t.ID):
			return nil, fmt.Errorf("%s:%d: id %q must match %s", path, line, t.ID, reID)
		case seen[t.ID]:
			return nil, fmt.Errorf("%s:%d: duplicate id %q", path, line, t.ID)
		case strings.TrimSpace(t.Prompt) == "":
			return nil, fmt.Errorf("%s:%d: task %s has an empty prompt", path, line, t.ID)
		}
		seen[t.ID] = true
		tasks = append(tasks, t)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%s: no tasks", path)
	}
	return tasks, sc.Err()
}

// StageInputs copies a task's input files into a fresh directory. Paths are
// relative to baseDir and may not escape it.
func StageInputs(baseDir string, inputs []string) (string, error) {
	dst, err := os.MkdirTemp("", "evh-task-")
	if err != nil {
		return "", err
	}
	for _, in := range inputs {
		clean := filepath.Clean(in)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			os.RemoveAll(dst)
			return "", fmt.Errorf("input %q must be a relative path inside the prompts directory", in)
		}
		src := filepath.Join(baseDir, clean)
		if err := copyPath(src, filepath.Join(dst, clean)); err != nil {
			os.RemoveAll(dst)
			return "", fmt.Errorf("input %q: %w", in, err)
		}
	}
	return dst, nil
}

func copyPath(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		default:
			return fmt.Errorf("%s is not a regular file or directory", p)
		}
	})
}

// LoadSkill builds a skill from skills/<name>/: its manifest, a runner on the
// host, and the agent-facing doc rendered from the template. It refuses a doc
// that leaks host paths or implementation details.
func LoadSkill(root, name string) (agents.Skill, error) {
	dir := filepath.Join(root, "skills", name)
	m, err := skills.LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return agents.Skill{}, fmt.Errorf("skill %s: %w", name, err)
	}
	tb, err := os.ReadFile(filepath.Join(dir, "SKILL.template.md"))
	if err != nil {
		return agents.Skill{}, fmt.Errorf("skill %s: %w", name, err)
	}
	doc, err := m.RenderDoc(string(tb))
	if err != nil {
		return agents.Skill{}, fmt.Errorf("skill %s: %w", name, err)
	}
	if hits := m.LeakCheck(doc); len(hits) > 0 {
		return agents.Skill{}, fmt.Errorf("skill %s: the agent-facing doc leaks: %s", name, strings.Join(hits, "; "))
	}
	return agents.Skill{Manifest: m, Runner: &skills.HostExec{Manifest: m}, Doc: doc}, nil
}

// Options configures a run.
type Options struct {
	Profile                                                   *config.Profile
	PromptsPath, OutDir, Root                                 string
	N                                                         int
	Seed                                                      int64
	FirecrackerBin, Kernel, BaseImage, Entries, ImageManifest string
	Upstream, APIKey                                          string // APIKey empty: fetched from pi on the host
	BudgetUSD                                                 float64
	Retries                                                   int // extra attempts for a trial that failed for infrastructure reasons
	Log                                                       io.Writer
}

// TrialSummary is one line of the run's result.
type TrialSummary struct {
	TaskID    string   `json:"task_id"`
	Trial     int      `json:"trial"`
	Status    string   `json:"status"`
	Steps     int      `json:"steps"`
	TokensIn  int      `json:"tokens_in"`
	TokensOut int      `json:"tokens_out"`
	CostUSD   float64  `json:"cost_usd"`
	WallS     float64  `json:"wall_s"`
	Flags     []string `json:"flags"`
	TracePath string   `json:"trace_path,omitempty"`
	Error     string   `json:"error,omitempty"`
	Attempts  int      `json:"attempts"` // 1 unless an infrastructure failure was retried
	Resolved  *bool    `json:"resolved,omitempty"`
	Reason    string   `json:"failure_reason,omitempty"` // the trace's failure.reason, when it has one
}

type Summary struct {
	RunID     string         `json:"run_id"`
	Dir       string         `json:"dir"`
	Trials    []TrialSummary `json:"trials"`
	TotalCost float64        `json:"total_cost_usd"`
	Stopped   string         `json:"stopped,omitempty"` // why the run ended early
}

// Run executes every task N times, sequentially, and writes a trace per trial.
func Run(ctx context.Context, o Options) (*Summary, error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.N < 1 {
		o.N = 1
	}
	tasks, err := LoadTasks(o.PromptsPath)
	if err != nil {
		return nil, err
	}
	// a coding task that failed validation would score the harness, not the agent
	kept := tasks[:0:0]
	for _, t := range tasks {
		if t.Verify != "" {
			if v, ok, err := verify.LoadValidation(filepath.Join(filepath.Dir(o.PromptsPath), t.Verify)); err != nil {
				return nil, err
			} else if !ok {
				fmt.Fprintf(o.Log, "warning: task %s has not been validated (run `harness validate`)\n", t.ID)
			} else if v.Status != "ok" {
				fmt.Fprintf(o.Log, "skipping task %s: failed validation (%s)\n", t.ID, strings.Join(v.Reasons, "; "))
				continue
			}
		}
		kept = append(kept, t)
	}
	if tasks = kept; len(tasks) == 0 {
		return nil, fmt.Errorf("no runnable tasks: every task failed validation")
	}
	var sk []agents.Skill
	for _, name := range o.Profile.Skills {
		s, err := LoadSkill(o.Root, name)
		if err != nil {
			return nil, err
		}
		sk = append(sk, s)
	}
	key := o.APIKey
	if key == "" {
		if o.Profile.API == "openai" {
			key, err = proxy.FetchOpencodeKey(o.Profile.Provider)
		} else {
			key, err = proxy.FetchKey(ctx, sandbox.NewSystem(), o.Profile.Provider, o.Profile.Model)
		}
		if err != nil {
			return nil, err
		}
	}
	if o.Upstream == "" {
		o.Upstream = o.Profile.Upstream
	}
	version := agentVersion(ctx, o.Profile.Agent)
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else {
		useSG = true
	}
	budget := o.BudgetUSD
	if budget == 0 {
		budget = o.Profile.BudgetUSD
	}

	sum := &Summary{RunID: "r-" + time.Now().UTC().Format("20060102-150405")}
	sum.Dir = filepath.Join(o.OutDir, sum.RunID)
	if err := os.MkdirAll(sum.Dir, 0o755); err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(o.PromptsPath)

	// Trials run on a worker pool sized by the profile's concurrency. Each trial has
	// its own VM, proxy and directories, so they only share the cost counter and the
	// skill runner, whose own lock keeps a heavy host tool to one call at a time.
	type job struct {
		idx   int
		task  Task
		trial int
	}
	var jobs []job
	for _, task := range tasks {
		for trial := 1; trial <= o.N; trial++ {
			jobs = append(jobs, job{len(jobs), task, trial})
		}
	}
	results := make([]*TrialSummary, len(jobs))
	cool := newBreaker(breakerFirst, breakerMax)
	var mu sync.Mutex
	next := 0
	refusals := 0                                                // consecutive refusals from the model API, guarded by mu
	overBudget := func() bool { return sum.TotalCost >= budget } // call with mu held
	workers := o.Profile.Concurrency
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if sum.Stopped != "" || next >= len(jobs) {
					mu.Unlock()
					return
				}
				j := jobs[next]
				switch {
				case overBudget():
					sum.Stopped = fmt.Sprintf("session budget of $%.2f reached before %s trial %d", budget, j.task.ID, j.trial)
					fmt.Fprintln(o.Log, sum.Stopped)
					mu.Unlock()
					return
				case ctx.Err() != nil:
					sum.Stopped = "interrupted"
					mu.Unlock()
					return
				}
				next++
				mu.Unlock()

				cool.wait(ctx)
				ts := runOneFn(ctx, o, j.task, j.trial, 0, sk, key, version, useSG, baseDir, sum)
				ts.Attempts = 1
				mu.Lock()
				sum.TotalCost += ts.CostUSD
				mu.Unlock()
				note := func(t TrialSummary) {
					if refused(t) {
						d := cool.trip(time.Now())
						mu.Lock()
						fmt.Fprintf(o.Log, "the model API refused %s trial %d; pausing new trials for %s\n", j.task.ID, j.trial, d.Round(time.Second))
						refusals++
						if limit := o.Profile.Limits.MaxRefusals; limit > 0 && refusals >= limit && sum.Stopped == "" {
							sum.Stopped = fmt.Sprintf("%s (%d refusals in a row)", RefusalStop, refusals)
							fmt.Fprintln(o.Log, sum.Stopped)
						}
						mu.Unlock()
					} else if t.Status != "infra_error" {
						cool.reset()
						mu.Lock()
						refusals = 0
						mu.Unlock()
					}
				}
				note(ts)
				// an infrastructure failure says nothing about the agent: try again, keeping
				// the failed attempt's trace as evidence, and report the last attempt
				for attempt := 1; attempt <= o.Retries && (ts.Status == "infra_error" || (ts.Error != "" && ts.TracePath == "")); attempt++ {
					mu.Lock()
					fmt.Fprintf(o.Log, "%-24s trial %d  infrastructure failure (%s %s), retrying (%d/%d)\n", j.task.ID, j.trial, ts.Status, ts.Error, attempt, o.Retries)
					stop := overBudget() || sum.Stopped != ""
					mu.Unlock()
					if stop {
						break
					}
					cool.wait(ctx)
					nx := runOneFn(ctx, o, j.task, j.trial, attempt, sk, key, version, useSG, baseDir, sum)
					nx.Attempts = ts.Attempts + 1
					mu.Lock()
					sum.TotalCost += nx.CostUSD
					mu.Unlock()
					note(nx)
					ts = nx
				}
				mu.Lock()
				results[j.idx] = &ts
				fmt.Fprintf(o.Log, "%-24s trial %d  %-15s steps=%-3d tokens=%d/%d  $%.4f  %.1fs  flags=%v %s\n",
					j.task.ID, j.trial, ts.Status, ts.Steps, ts.TokensIn, ts.TokensOut, ts.CostUSD, ts.WallS, ts.Flags, ts.Error)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r != nil {
			sum.Trials = append(sum.Trials, *r)
		}
	}
	return sum, writeSummary(sum)
}

// breaker pauses the whole run when the model API refuses requests (a rate limit or a
// used-up quota). Starting more trials into a refusal only wastes them; waiting lets
// the limit clear. Each refusal in a row doubles the pause, up to a ceiling, and any
// trial that gets answers resets it.
type breaker struct {
	mu         sync.Mutex
	until      time.Time
	pause      time.Duration
	first, max time.Duration
}

func newBreaker(first, max time.Duration) *breaker { return &breaker{first: first, max: max} }

func (b *breaker) trip(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pause == 0 {
		b.pause = b.first
	} else if b.pause *= 2; b.pause > b.max {
		b.pause = b.max
	}
	if until := now.Add(b.pause); until.After(b.until) {
		b.until = until
	}
	return b.pause
}

func (b *breaker) reset() {
	b.mu.Lock()
	b.pause = 0
	b.mu.Unlock()
}

// wait blocks until the pause is over or ctx ends.
func (b *breaker) wait(ctx context.Context) {
	for {
		b.mu.Lock()
		d := time.Until(b.until)
		b.mu.Unlock()
		if d <= 0 {
			return
		}
		select {
		case <-time.After(min(d, 30*time.Second)):
		case <-ctx.Done():
			return
		}
	}
}

// RefusalStop starts the Summary.Stopped reason when the run ends because the model API
// refused too many trials in a row (limits.max_refusals). The CLI keys its exit code on it.
const RefusalStop = "stopped early: model API refusing"

// refused reports a trial that ended because the model API refused it.
func refused(ts TrialSummary) bool {
	return ts.Status == "infra_error" && ts.Reason == "upstream"
}

// breakerFirst and breakerMax are the cooldown steps; tests shorten them.
var breakerFirst, breakerMax = 5 * time.Minute, 40 * time.Minute

// runOneFn is runOne, replaceable so the scheduling can be tested without VMs.
var runOneFn = runOne

func runOne(ctx context.Context, o Options, task Task, trial, attempt int, sk []agents.Skill, key, version string, useSG bool, baseDir string, sum *Summary) TrialSummary {
	ts := TrialSummary{TaskID: task.ID, Trial: trial, Flags: []string{}}
	prof := *o.Profile
	if task.Limits != nil {
		if task.Limits.WallS > 0 {
			prof.Limits.WallS = task.Limits.WallS
		}
		if task.Limits.Tokens > 0 {
			prof.Limits.Tokens = task.Limits.Tokens
		}
	}
	inputs, err := StageInputs(baseDir, task.Inputs)
	if err != nil {
		ts.Error = err.Error()
		return ts
	}
	defer os.RemoveAll(inputs)
	var (
		envImage, envEntries, repoDir, keepUpper string
		pathPrefix                               []string
	)
	if task.Env != "" || task.Workspace != "" {
		if repoDir, envImage, envEntries, err = taskDirs(o.Root, baseDir, task); err != nil {
			ts.Error = err.Error()
			return ts
		}
		if task.Env != "" {
			pathPrefix = []string{"/opt/env/venv/bin"}
		}
		os.RemoveAll(inputs)
		inputs = ""
		if inputs, err = StageInputs(repoDir, []string{"."}); err != nil {
			ts.Error = err.Error()
			return ts
		}
		defer os.RemoveAll(inputs)
		if task.Verify != "" {
			if keepUpper, err = os.MkdirTemp("", "evh-upper-"); err != nil {
				ts.Error = err.Error()
				return ts
			}
			defer os.RemoveAll(keepUpper)
		}
	}

	taskDir := filepath.Join(sum.Dir, task.ID)
	trialName := fmt.Sprintf("trial-%d", trial)
	if attempt > 0 {
		trialName = fmt.Sprintf("trial-%d-retry%d", trial, attempt)
	}
	res, err := agents.RunTrial(ctx, agents.Spec{
		RunsDir: taskDir, TrialID: trialName, TaskID: task.ID, RunID: sum.RunID, Trial: trial, Seed: o.Seed + int64(trial),
		Prompt: task.Prompt, Profile: &prof, InputsDir: inputs, Skills: sk,
		FirecrackerBin: o.FirecrackerBin, Kernel: o.Kernel, BaseImage: o.BaseImage, UseSG: useSG,
		ImageEntries: o.Entries, ImageManifest: o.ImageManifest, AgentVersion: version, Upstream: o.Upstream, APIKey: key,
		EnvImage: envImage, EnvEntries: envEntries, PathPrefix: pathPrefix, KeepUpper: keepUpper,
	})
	trialDir := filepath.Join(taskDir, trialName)
	if res != nil && res.Console != "" {
		_ = os.WriteFile(filepath.Join(trialDir, "console.log"), []byte(res.Console), 0o600)
	}
	_ = os.Remove(filepath.Join(trialDir, "workspace.ext4"))
	if err != nil || res == nil || res.Trace == nil {
		if err == nil {
			err = fmt.Errorf("no trace produced")
		}
		ts.Error = err.Error()
		_ = os.WriteFile(filepath.Join(trialDir, "error.txt"), []byte(err.Error()+"\n"), 0o600)
		return ts
	}
	tr := res.Trace
	if task.Verify != "" && repoDir != "" && tr.Status != "infra_error" {
		tr.Verify = runVerify(ctx, o, task, baseDir, repoDir, envImage, envEntries, keepUpper, res.Changes, filepath.Join(trialDir, "verify"), useSG)
		if tr.Verify.Status == "infra_error" {
			ts.Error = "verify: " + tr.Verify.Cause
		}
	}
	b, err := trace.Encode(tr)
	if err != nil {
		ts.Error = "trace invalid: " + err.Error()
		return ts
	}
	ts.TracePath = filepath.Join(trialDir, "trace.json")
	if err := os.WriteFile(ts.TracePath, b, 0o644); err != nil {
		ts.Error = err.Error()
		return ts
	}
	if tr.Verify != nil && tr.Verify.Status != "infra_error" {
		r := tr.Verify.Resolved
		ts.Resolved = &r
	}
	if tr.Failure != nil {
		ts.Reason = tr.Failure.Reason
	}
	ts.Status, ts.Steps, ts.WallS = tr.Status, tr.Metrics.Steps, tr.Metrics.WallS
	ts.TokensIn, ts.TokensOut, ts.CostUSD = tr.Metrics.TokensIn, tr.Metrics.TokensOut, tr.Metrics.CostUSD
	seen := map[string]bool{}
	for _, f := range tr.Provenance.Flags {
		if !seen[f.Code] {
			seen[f.Code] = true
			ts.Flags = append(ts.Flags, f.Code)
		}
	}
	return ts
}

// runVerify runs the hidden tests against the agent's final files, retrying a
// harness fault once. A fault is reported as infra_error, never as unresolved.
func runVerify(ctx context.Context, o Options, task Task, baseDir, repoDir, envImage, envEntries, upper string, changes []sandbox.Change, dir string, useSG bool) *trace.Verify {
	vdir := filepath.Join(baseDir, task.Verify)
	tests, err := verify.LoadTests(vdir)
	if err != nil {
		return &trace.Verify{Status: "infra_error", Cause: "infra", ChangedFiles: []string{}, Attempts: 1}
	}
	var testPatch []byte
	if tests.Kind != "script" {
		if testPatch, err = os.ReadFile(filepath.Join(vdir, "test_patch.diff")); err != nil {
			return &trace.Verify{Status: "infra_error", Cause: "infra", ChangedFiles: []string{}, Attempts: 1}
		}
	}
	wall := 900
	if o.Profile.Limits.WallS > 0 && o.Profile.Limits.WallS < wall {
		wall = o.Profile.Limits.WallS
	}
	var v *trace.Verify
	for attempt := 1; attempt <= 2; attempt++ {
		v, err = verify.Run(ctx, verify.Spec{Dir: dir, Tests: tests, TestPatch: testPatch, RepoDir: repoDir, UpperDir: upper, Changes: changes,
			ScriptPath: filepath.Join(vdir, "verify.sh"), EnvImage: envImage, EnvEntries: envEntries, PathPrefix: pathPrefixFor(envImage),
			FirecrackerBin: o.FirecrackerBin, Kernel: o.Kernel, BaseImage: o.BaseImage, ImageEntries: o.Entries, UseSG: useSG,
			MemMiB: o.Profile.VMMemMiB, WallS: wall})
		if v == nil {
			v = &trace.Verify{Status: "infra_error", Cause: "infra", ChangedFiles: []string{}}
		}
		v.Attempts = attempt
		if err == nil || v.Cause == "test_patch_failed" {
			break
		}
	}
	return v
}

func writeSummary(s *Summary) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "summary.json"), append(b, '\n'), 0o644)
}

func agentVersion(ctx context.Context, agent string) string {
	out, err := sandbox.NewSystem().Run(ctx, agent, "--version")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
}

// pathPrefixFor is the toolchain directory to put first on PATH: the python venv
// when the task has a toolchain layer.
func pathPrefixFor(envImage string) []string {
	if envImage == "" {
		return nil
	}
	return []string{"/opt/env/venv/bin"}
}
