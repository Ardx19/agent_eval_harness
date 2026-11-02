// Package verify decides whether an agent's change resolves a task. The agent's
// final files are copied onto a pristine checkout, the hidden tests are applied,
// and the tests run in a second microVM that never saw the agent. The verdict is
// measured by the host from that run; nothing the agent said is consulted.
package verify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentevalharness/internal/agents"
	"agentevalharness/internal/guest"
	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/trace"
	"agentevalharness/internal/vm"
)

// Tests is the host-only description of a task's hidden tests (tests.json).
type Tests struct {
	InstanceID  string   `json:"instance_id"`
	Python      string   `json:"python"`
	TestFiles   []string `json:"test_files"`
	FailToPass  []string `json:"fail_to_pass"`
	PassToPass  []string `json:"pass_to_pass"`
	TestCommand []string `json:"test_command,omitempty"` // default: pytest -rA over TestFiles

	// Kind "script" runs a hidden verify.sh (next to tests.json) instead of pytest: it
	// prints "PASS <name>" or "FAIL <name>" per check, and fail_to_pass lists the names
	// that must pass. The workspace need not be a git repository.
	Kind string `json:"kind,omitempty"`

	// DoNothingOK marks a task whose correct outcome is to change nothing and say it
	// cannot be done (a request for data that does not exist). The untouched workspace
	// must then pass, and validation requires it to.
	DoNothingOK bool `json:"do_nothing_ok,omitempty"`

	// P2PExcluded lists pass-to-pass tests that fail in our environment even with the
	// gold patch (loaded from validated.json, written by `harness validate`). They
	// are left out of the verdict and counted in the trace.
	P2PExcluded []string `json:"-"`
}

// Validation is what `harness validate` learned about a task (validated.json).
type Validation struct {
	Status        string   `json:"status"` // ok | invalid
	Reasons       []string `json:"reasons,omitempty"`
	GoldRuns      int      `json:"gold_runs"`
	GoldResolved  int      `json:"gold_resolved"`
	EmptyResolved bool     `json:"empty_resolved"`
	P2PExcluded   []string `json:"p2p_excluded,omitempty"`
	WallS         float64  `json:"gold_wall_s"`
}

// LoadValidation reads <dir>/validated.json; ok is false when the task was never validated.
func LoadValidation(dir string) (v Validation, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "validated.json"))
	if os.IsNotExist(err) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	return v, true, json.Unmarshal(b, &v)
}

// RunTargets is what pytest is pointed at: the test modules in the test patch.
// A patch can also add data files (tests/static/config.toml), which are applied
// but are not test modules.
func (t *Tests) RunTargets() []string {
	var out, py []string
	for _, f := range t.TestFiles {
		if !strings.HasSuffix(f, ".py") {
			continue
		}
		py = append(py, f)
		if b := filepath.Base(f); strings.HasPrefix(b, "test_") || strings.HasSuffix(b, "_test.py") {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return py
	}
	return out
}

// LoadTests reads <dir>/tests.json.
func LoadTests(dir string) (*Tests, error) {
	b, err := os.ReadFile(filepath.Join(dir, "tests.json"))
	if err != nil {
		return nil, err
	}
	var t Tests
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s/tests.json: %w", dir, err)
	}
	if t.Kind == "script" {
		if len(t.FailToPass) == 0 {
			return nil, fmt.Errorf("%s/tests.json: a script task needs fail_to_pass (the check names)", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "verify.sh")); err != nil {
			return nil, fmt.Errorf("%s: a script task needs verify.sh next to tests.json", dir)
		}
	} else if len(t.TestFiles) == 0 || len(t.FailToPass) == 0 {
		return nil, fmt.Errorf("%s/tests.json: needs test_files and fail_to_pass", dir)
	}
	if v, ok, err := LoadValidation(dir); err != nil {
		return nil, err
	} else if ok {
		t.P2PExcluded = v.P2PExcluded
	}
	return &t, nil
}

// Spec is one verify run.
type Spec struct {
	Dir                                             string // host directory for this run's scratch files (created)
	Tests                                           *Tests
	TestPatch                                       []byte
	RepoDir                                         string // pristine checkout the agent started from
	UpperDir                                        string // dumped overlay upper of the agent's VM ("" or missing = no changes)
	Changes                                         []sandbox.Change
	AgentPatch                                      []byte // alternative to UpperDir/Changes: a patch standing in for the agent (validate uses the gold patch)
	ScriptPath                                      string // hidden verify.sh for tests of kind "script"
	AgentTree                                       string // alternative to AgentPatch for script tasks: a directory of final files overlaid on the workspace (keeps modes and symlinks)
	EnvImage                                        string
	EnvEntries                                      string
	PathPrefix                                      []string
	FirecrackerBin, Kernel, BaseImage, ImageEntries string
	UseSG                                           bool
	MemMiB, WallS                                   int
	IdleS                                           int // end the run after this long without output; 0 = never (used by tests)
}

const projectPrefix = "/home/dev/project/"

// noise paths never count as the agent's work
var noiseDirs = []string{"/.git/", "/__pycache__/", "/.pytest_cache/", "/.hypothesis/", "/.mypy_cache/", "/.ruff_cache/"}

func isNoise(rel string) bool {
	p := "/" + rel
	for _, n := range noiseDirs {
		if strings.Contains(p+"/", n) {
			return true
		}
	}
	return strings.HasSuffix(rel, ".pyc")
}

// tampering reports changed paths that could alter how the hidden tests run.
func tampering(files, testFiles []string) []string {
	test := map[string]bool{}
	for _, f := range testFiles {
		test[f] = true
	}
	var out []string
	for _, f := range files {
		if test[f] || isConfig(f) {
			out = append(out, f)
		}
	}
	return out
}

// configNames are files that change how pytest starts or reports. Like the
// hidden tests, they are restored to the base before the run: a repository fix
// does not need them, and a cheat can hide in them.
var configNames = map[string]bool{"conftest.py": true, "pytest.ini": true, "tox.ini": true, "setup.cfg": true, "pyproject.toml": true,
	"sitecustomize.py": true, "usercustomize.py": true}

func isConfig(rel string) bool {
	return configNames[filepath.Base(rel)] || strings.HasSuffix(rel, ".pth")
}

// resetConfig restores every config-like file under dst to its base content, or
// removes it when the base has none. It returns the paths it changed.
func resetConfig(dst string) ([]string, error) {
	var changed []string
	err := filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dst, p)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isConfig(rel) {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, err := git(dst, "cat-file", "-e", "HEAD:"+rel); err != nil {
			changed = append(changed, rel)
			return os.Remove(p)
		}
		if out, _ := git(dst, "status", "--porcelain", "--", rel); len(strings.TrimSpace(string(out))) > 0 {
			changed = append(changed, rel)
			_, err := git(dst, "checkout", "HEAD", "--", rel)
			return err
		}
		return nil
	})
	sort.Strings(changed)
	return changed, err
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.fileMode=true", "-c", "user.name=dev", "-c", "user.email=dev@localhost"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func copyDir(src, dst string) error {
	out, err := exec.Command("cp", "-a", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	return nil
}

// BuildTree makes the tree the hidden tests run against: the pristine checkout,
// the agent's changes, test files reset to the base, then the test patch. It also
// returns the agent's change as a patch.
func BuildTree(s Spec, dst string) (patch []byte, files []string, err error) {
	// git runs inside the tree, so every path handed to it must be absolute
	if dst, err = filepath.Abs(dst); err != nil {
		return nil, nil, err
	}
	if err := copyDir(s.RepoDir, dst); err != nil {
		return nil, nil, err
	}
	if err := overlayChanges(s, dst); err != nil {
		return nil, nil, err
	}
	if len(s.AgentPatch) > 0 {
		pp := filepath.Join(filepath.Dir(dst), "agent.patch")
		if err := os.WriteFile(pp, s.AgentPatch, 0o600); err != nil {
			return nil, nil, err
		}
		if _, err := git(dst, "apply", "--whitespace=nowarn", pp); err != nil {
			return nil, nil, fmt.Errorf("agent patch does not apply: %w", err)
		}
	}
	// the agent's change as a patch (ignored files such as build artifacts stay out)
	if _, err := git(dst, "add", "-A", "-N"); err != nil {
		return nil, nil, err
	}
	if patch, err = git(dst, "diff", "--binary", "HEAD"); err != nil {
		return nil, nil, err
	}
	names, err := git(dst, "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, nil, err
	}
	for _, n := range strings.Split(strings.TrimSpace(string(names)), "\n") {
		if n != "" {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	// config-like files are restored to the base (recorded in tampering below)
	if _, err := resetConfig(dst); err != nil {
		return patch, files, err
	}
	// hidden tests replace whatever the agent did to those files
	for _, tf := range s.Tests.TestFiles {
		if _, err := git(dst, "cat-file", "-e", "HEAD:"+tf); err == nil {
			if _, err := git(dst, "checkout", "HEAD", "--", tf); err != nil {
				return patch, files, err
			}
		} else {
			_ = os.Remove(filepath.Join(dst, tf))
		}
	}
	tp := filepath.Join(filepath.Dir(dst), "test_patch.diff")
	if err := os.WriteFile(tp, s.TestPatch, 0o600); err != nil {
		return patch, files, err
	}
	if _, err := git(dst, "apply", "--whitespace=nowarn", tp); err != nil {
		return patch, files, fmt.Errorf("test_patch_failed: %w", err)
	}
	return patch, files, nil
}

// overlayChanges copies the agent's final files from its dumped upper layer onto
// dst (a copy of the pristine workspace) and applies its deletions. Noise paths
// (.git, caches, bytecode) are skipped.
func overlayChanges(s Spec, dst string) error {
	upper := filepath.Join(s.UpperDir, "upper")
	for _, c := range s.Changes {
		if !strings.HasPrefix(c.Path, projectPrefix) {
			continue
		}
		rel := strings.TrimPrefix(c.Path, projectPrefix)
		if rel == "" || isNoise(rel) {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		src := filepath.Join(upper, filepath.FromSlash(strings.TrimPrefix(c.Path, "/")))
		switch {
		case c.Op == "delete":
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		case c.Kind == "dir":
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case c.Kind == "file" && (c.Op == "create" || c.ContentChanged || c.Perm != 0):
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := copyDir(src, target); err != nil {
				return fmt.Errorf("copy %s: %w", c.Path, err)
			}
			_ = os.Chmod(target, os.FileMode(c.Perm&0o777))
		case c.Kind == "symlink":
			_ = os.RemoveAll(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(c.Link, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func randKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return []byte(hex.EncodeToString(b))
}

// normID is the key tests are matched by. The dataset's ids come from a log
// parser that keeps only the first whitespace-separated token, so a parametrised
// id such as "t.py::test[a b]" is recorded as "t.py::test[a"; match the same way.
func normID(id string) string {
	if f := strings.Fields(id); len(f) > 0 {
		return f[0]
	}
	return id
}

// ParseOutcomes reads pytest's "-rA" summary lines into test id -> PASSED|FAILED.
func ParseOutcomes(lines []string) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		for _, st := range []string{"PASSED", "XFAIL", "FAILED", "ERROR"} {
			if !strings.HasPrefix(l, st+" ") {
				continue
			}
			id := strings.TrimSpace(strings.TrimPrefix(l, st+" "))
			if st == "FAILED" || st == "ERROR" {
				if i := strings.Index(id, " - "); i >= 0 {
					id = id[:i]
				}
			}
			id = normID(id)
			status := "PASSED"
			if st == "FAILED" || st == "ERROR" {
				status = "FAILED"
			}
			// a test reported both ways (e.g. a teardown error) counts as failed
			if out[id] != "FAILED" {
				out[id] = status
			}
			break
		}
	}
	return out
}

// Judge scores parsed outcomes against the task's lists.
func Judge(t *Tests, got map[string]string, patchEmpty bool) (v trace.Verify) {
	v.F2P, v.P2P = map[string]string{}, map[string]string{}
	status := func(id string) string {
		if s, ok := got[normID(id)]; ok {
			return s
		}
		return "MISSING"
	}
	f2pOK, p2pOK := true, true
	for _, id := range t.FailToPass {
		v.F2P[id] = status(id)
		if v.F2P[id] != "PASSED" {
			f2pOK = false
		}
	}
	excluded := map[string]bool{}
	for _, id := range t.P2PExcluded {
		excluded[id] = true
	}
	for _, id := range t.PassToPass {
		if excluded[id] {
			v.P2PExcluded++
			continue
		}
		v.P2P[id] = status(id)
		v.P2PTotal++
		if v.P2P[id] != "PASSED" {
			v.P2PFailed++
			p2pOK = false
		}
	}
	switch {
	case f2pOK && p2pOK:
		v.Status, v.Resolved = "resolved", true
	case patchEmpty:
		v.Status, v.Cause = "unresolved", "empty_patch"
	case !f2pOK:
		v.Status, v.Cause = "unresolved", "f2p_failed"
	default:
		v.Status, v.Cause = "unresolved", "p2p_broken"
	}
	return v
}

// vmOutcome is what a verify VM produced.
type vmOutcome struct {
	Lines []string
	Exit  *guest.Frame
	WallS float64
}

// runInVM boots a fresh VM over tree (the workspace) and the env layer, runs argv
// as the agent uid, and returns its output lines and exit frame. It writes
// console.log and output.log under s.Dir. A VM that does not finish cleanly is an
// error, never a verdict.
func runInVM(ctx context.Context, s Spec, tree string, argv []string, extra []sandbox.ExtraFile) (vmOutcome, error) {
	var out vmOutcome
	ctlKey := randKey()
	spec := guest.RunSpec{Argv: argv, Dir: "/home/dev/project", UID: 1000, GID: 1000, WallS: s.WallS, IdleS: s.IdleS, Listen: "127.0.0.1:18080",
		Env: []string{"HOME=/home/dev", "USER=dev", "LOGNAME=dev", "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1", "PYTHONDONTWRITEBYTECODE=1",
			"PATH=" + agents.GuestPath(s.PathPrefix)}}
	specJSON, _ := json.Marshal(spec)
	root := 0
	extras := append([]sandbox.ExtraFile{
		{Path: guest.RunSpecPath, Data: specJSON, Mode: 0o400, Owner: &root, ParentMode: "0700"},
		{Path: guest.CtlKeyPath, Data: ctlKey, Mode: 0o400, Owner: &root, ParentMode: "0700"},
	}, extra...)
	sys := sandbox.NewSystem()
	wsImg := filepath.Join(s.Dir, "workspace.ext4")
	if _, err := sandbox.BuildWorkspaceWith(ctx, sys, tree, wsImg, sandbox.DefaultIdentities, extras); err != nil {
		return out, fmt.Errorf("verify workspace: %w", err)
	}
	defer os.Remove(wsImg)
	cfg := vm.Config{FirecrackerBin: s.FirecrackerBin, Kernel: s.Kernel, BaseImage: s.BaseImage, Init: "/guest-init",
		RunDir: filepath.Join(s.Dir, "vm"), VCPUs: 2, MemMiB: s.MemMiB, ScratchMiB: 256, WorkspaceImage: wsImg,
		EnvImage: s.EnvImage, WallS: s.WallS + 60, UseSG: s.UseSG}
	machine, err := vm.New(cfg)
	if err != nil {
		return out, err
	}
	defer machine.Destroy()
	ctl := agents.NewControl(ctlKey)
	if err := machine.Listen(guest.ControlPort, ctl.ServeConn); err != nil {
		return out, err
	}
	start := time.Now()
	if err := machine.Start(ctx); err != nil {
		return out, err
	}
	res, werr := machine.Wait(ctx)
	out.WallS = time.Since(start).Seconds()
	_ = os.WriteFile(filepath.Join(s.Dir, "console.log"), []byte(machine.Console()), 0o600)
	out.Lines = ctl.Lines(guest.StreamStdout)
	_ = os.WriteFile(filepath.Join(s.Dir, "output.log"), []byte(strings.Join(append(append([]string{}, out.Lines...), ctl.Lines(guest.StreamStderr)...), "\n")), 0o644)
	out.Exit = ctl.Exit()
	if werr != nil || res.Exit != vm.ExitOK || out.Exit == nil {
		return out, fmt.Errorf("verify VM did not finish cleanly (exit %v, err %v, exit frame %v)", res.Exit, werr, out.Exit != nil)
	}
	return out, nil
}

// Run builds the tree, boots the verify VM and judges the result. A harness
// fault returns Status infra_error with the cause, never "unresolved".
func Run(ctx context.Context, s Spec) (*trace.Verify, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if s.Tests.Kind == "script" {
		return runScript(ctx, s)
	}
	tree := filepath.Join(s.Dir, "tree")
	_ = os.RemoveAll(tree)
	patch, files, err := BuildTree(s, tree)
	v := &trace.Verify{ChangedFiles: files, Attempts: 1}
	if files == nil {
		v.ChangedFiles = []string{}
	}
	sum := sha256.Sum256(patch)
	v.PatchSHA256, v.PatchBytes = hex.EncodeToString(sum[:]), len(patch)
	v.Tampered = tampering(files, s.Tests.TestFiles)
	_ = os.WriteFile(filepath.Join(s.Dir, "model.patch"), patch, 0o644)
	if err != nil {
		v.Status, v.Cause, v.OutputTail = "infra_error", "test_patch_failed", err.Error()
		if !strings.HasPrefix(err.Error(), "test_patch_failed") {
			v.Cause = "infra"
		}
		return v, err
	}

	canary, err := AddCanary(tree)
	if err != nil {
		v.Status, v.Cause = "infra_error", "infra"
		return v, err
	}
	targets := append(s.Tests.RunTargets(), canary.File)
	argv := s.Tests.TestCommand
	if len(argv) == 0 {
		argv = append([]string{"/opt/env/venv/bin/python", "-m", "pytest", "-rA", "-p", "no:cacheprovider", "-q", "--color=no"}, targets...)
	}
	out, err := runInVM(ctx, s, tree, argv, nil)
	v.WallS = out.WallS
	v.OutputTail = tail(out.Lines, 4096)
	if err != nil {
		v.Status, v.Cause = "infra_error", "infra"
		return v, err
	}
	if out.Exit.Reason == "timeout" || out.Exit.Reason == "idle" {
		v.Status, v.Cause, v.ExitCode = "unresolved", "verify_timeout", out.Exit.Code
		return v, nil
	}
	v.ExitCode = out.Exit.Code
	lines := out.Lines
	outcomes := ParseOutcomes(lines)
	j := Judge(s.Tests, outcomes, len(files) == 0)
	if in := CheckIntegrity(lines, outcomes, canary, out.Exit.Code, s.Tests.RunTargets()); !in.OK {
		j.Status, j.Resolved, j.Cause, j.Note = "compromised", false, "reporting_tampered", in.Reason
	}
	j.CanaryOK = outcomes[normID(canary.failID())] == "FAILED" && out.Exit.Code != 0
	j.ChangedFiles, j.PatchSHA256, j.PatchBytes, j.Tampered = v.ChangedFiles, v.PatchSHA256, v.PatchBytes, v.Tampered
	j.ExitCode, j.WallS, j.OutputTail, j.Attempts = v.ExitCode, v.WallS, v.OutputTail, 1
	return &j, nil
}

func tail(lines []string, max int) string {
	n, from := 0, len(lines)
	for from > 0 && n < max {
		from--
		n += len(lines[from]) + 1
	}
	return strings.Join(lines[from:], "\n")
}
