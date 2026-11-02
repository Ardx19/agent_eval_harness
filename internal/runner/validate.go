package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentevalharness/internal/trace"
	"agentevalharness/internal/verify"
)

// ValidateOptions configures `harness validate`.
type ValidateOptions struct {
	Options
	GoldRuns int // times each task's gold patch is verified (flakiness check)
}

// Validate checks every coding task before it counts: the gold patch must resolve
// it every time and an empty patch must not. Tests that fail in our environment
// even with the gold patch are excluded from the verdict and recorded. Results go
// to <verify dir>/validated.json, which `harness run` consults.
func Validate(ctx context.Context, o ValidateOptions) (map[string]verify.Validation, error) {
	tasks, err := LoadTasks(o.PromptsPath)
	if err != nil {
		return nil, err
	}
	if o.GoldRuns < 1 {
		o.GoldRuns = 3
	}
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else {
		useSG = true
	}
	baseDir := filepath.Dir(o.PromptsPath)
	results := map[string]verify.Validation{}
	for _, task := range tasks {
		if (task.Env == "" && task.Workspace == "") || task.Verify == "" {
			continue
		}
		v := validateOne(ctx, o, task, baseDir, useSG)
		results[task.ID] = v
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(filepath.Join(baseDir, task.Verify, "validated.json"), append(b, '\n'), 0o644); err != nil {
			return results, err
		}
		fmt.Fprintf(o.Log, "%-28s %-8s gold %d/%d  empty_resolved=%v  p2p_excluded=%d  %s\n", task.ID, v.Status, v.GoldResolved, v.GoldRuns, v.EmptyResolved,
			len(v.P2PExcluded), strings.Join(v.Reasons, "; "))
		if ctx.Err() != nil {
			break
		}
	}
	return results, nil
}

func validateOne(ctx context.Context, o ValidateOptions, task Task, baseDir string, useSG bool) verify.Validation {
	v := verify.Validation{Status: "invalid", GoldRuns: o.GoldRuns}
	fail := func(format string, a ...any) verify.Validation {
		v.Reasons = append(v.Reasons, fmt.Sprintf(format, a...))
		return v
	}
	vdir := filepath.Join(baseDir, task.Verify)
	tests, err := verify.LoadTests(vdir)
	if err != nil {
		return fail("tests.json: %v", err)
	}
	tests.P2PExcluded = nil // measure from scratch
	var testPatch []byte
	goldTree := ""
	if st, e := os.Stat(filepath.Join(vdir, "gold")); e == nil && st.IsDir() && tests.Kind == "script" {
		goldTree = filepath.Join(vdir, "gold")
	}
	gold, err2 := os.ReadFile(filepath.Join(vdir, "gold.patch"))
	if goldTree != "" {
		err2 = nil
	}
	if tests.Kind != "script" {
		var err1 error
		if testPatch, err1 = os.ReadFile(filepath.Join(vdir, "test_patch.diff")); err1 != nil {
			return fail("missing test_patch.diff")
		}
	}
	if err2 != nil {
		return fail("missing gold.patch")
	}
	repoDir, envImage, _, derr := taskDirs(o.Root, baseDir, task)
	if derr != nil {
		return fail("%v", derr)
	}
	spec := func(name string, patch []byte) verify.Spec {
		return verify.Spec{Dir: filepath.Join(o.OutDir, "validate", task.ID, name), Tests: tests, TestPatch: testPatch,
			RepoDir: repoDir, AgentPatch: patch, AgentTree: treeFor(patch, goldTree), ScriptPath: filepath.Join(vdir, "verify.sh"), EnvImage: envImage,
			PathPrefix: pathPrefixFor(envImage), FirecrackerBin: o.FirecrackerBin, Kernel: o.Kernel, BaseImage: o.BaseImage,
			ImageEntries: o.Entries, UseSG: useSG, MemMiB: o.Profile.VMMemMiB, WallS: 900}
	}

	var golds []*trace.Verify
	for i := 1; i <= o.GoldRuns; i++ {
		r, err := verify.Run(ctx, spec(fmt.Sprintf("gold-%d", i), gold))
		if err != nil || r == nil || r.Status == "infra_error" {
			return fail("gold run %d failed to run: %v", i, err)
		}
		golds = append(golds, r)
		v.WallS += r.WallS
	}
	// a test is excluded when it fails in every gold run, flaky when it fails in some
	failures := map[string]int{}
	for _, r := range golds {
		for id, st := range r.P2P {
			if st != "PASSED" {
				failures[id]++
			}
		}
	}
	var flaky []string
	for id, n := range failures {
		if n == len(golds) {
			v.P2PExcluded = append(v.P2PExcluded, id)
		} else {
			flaky = append(flaky, id)
		}
	}
	sort.Strings(v.P2PExcluded)
	sort.Strings(flaky)
	for _, r := range golds {
		f2p := true
		for _, st := range r.F2P {
			if st != "PASSED" {
				f2p = false
			}
		}
		other := 0
		for id, st := range r.P2P {
			if st != "PASSED" && !contains(v.P2PExcluded, id) {
				other++
			}
		}
		if f2p && other == 0 {
			v.GoldResolved++
		}
	}
	total := len(tests.PassToPass)
	switch {
	case v.GoldResolved != v.GoldRuns:
		fail("the gold patch resolved %d of %d runs (flaky or failing tests: %s)", v.GoldResolved, v.GoldRuns, strings.Join(append(append([]string{}, flaky...), firstFailedF2P(golds)...), ", "))
	case total > 0 && len(v.P2PExcluded)*10 > total:
		fail("%d of %d pass-to-pass tests fail even with the gold patch", len(v.P2PExcluded), total)
	}
	empty, err := verify.Run(ctx, spec("empty", nil))
	if err != nil || empty == nil || empty.Status == "infra_error" {
		fail("empty-patch run failed to run: %v", err)
	} else {
		resolved, reason := emptyRunVerdict(tests.DoNothingOK, empty.F2P)
		v.EmptyResolved = resolved
		if reason != "" {
			fail("%s", reason)
		}
	}
	if len(v.Reasons) == 0 {
		v.Status = "ok"
	}
	return v
}

func firstFailedF2P(rs []*trace.Verify) []string {
	var out []string
	for _, r := range rs {
		for id, st := range r.F2P {
			if st != "PASSED" {
				out = append(out, id)
			}
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// treeFor passes the gold tree only for runs that carry the gold solution (a nil
// patch is the "do nothing" run, which must not receive it).
func treeFor(patch []byte, goldTree string) string {
	if patch == nil {
		return ""
	}
	return goldTree
}

// emptyRunVerdict judges the run in which the agent changes nothing. Normally it must
// fail (otherwise the task is already solved); for a task where doing nothing is the
// right answer it must pass. resolved reports whether all fail-to-pass checks passed.
func emptyRunVerdict(doNothingOK bool, f2p map[string]string) (resolved bool, problem string) {
	resolved = true
	for _, st := range f2p {
		if st != "PASSED" {
			resolved = false
		}
	}
	switch {
	case doNothingOK && !resolved:
		return resolved, "doing nothing is the correct answer for this task, but its checks fail on the untouched workspace"
	case !doNothingOK && resolved:
		return resolved, "the fail-to-pass tests already pass without any change"
	}
	return resolved, ""
}
