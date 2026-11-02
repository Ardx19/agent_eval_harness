package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/trace"
)

// scriptGuestPath is where the hidden script appears in the verify VM only: a
// root-owned, read-only file the agent never had.
const scriptGuestPath = "/usr/share/agent-skills/_verify/verify.sh"

// ParseScriptOutcomes reads "PASS <name>" and "FAIL <name>" lines. A name reported
// both ways counts as failed.
func ParseScriptOutcomes(lines []string) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		for _, st := range []string{"PASS", "FAIL"} {
			if strings.HasPrefix(l, st+" ") {
				id := strings.TrimSpace(strings.TrimPrefix(l, st+" "))
				if id == "" {
					continue
				}
				if st == "FAIL" {
					out[id] = "FAILED"
				} else if out[id] != "FAILED" {
					out[id] = "PASSED"
				}
			}
		}
	}
	return out
}

// runScript verifies a task whose hidden check is a shell script: the agent's final
// files are copied onto the pristine workspace and the script runs in a fresh VM.
func runScript(ctx context.Context, s Spec) (*trace.Verify, error) {
	tree := filepath.Join(s.Dir, "tree")
	_ = os.RemoveAll(tree)
	v := &trace.Verify{ChangedFiles: []string{}, Attempts: 1}
	fail := func(cause string, err error) (*trace.Verify, error) {
		v.Status, v.Cause = "infra_error", cause
		return v, err
	}
	if s.ScriptPath == "" {
		return fail("infra", fmt.Errorf("script task without a script"))
	}
	script, err := os.ReadFile(s.ScriptPath)
	if err != nil {
		return fail("infra", err)
	}
	if out, err := exec.Command("cp", "-a", s.RepoDir, tree).CombinedOutput(); err != nil {
		return fail("infra", fmt.Errorf("cp: %v: %s", err, out))
	}
	if s.AgentTree != "" {
		if out, err := exec.Command("cp", "-a", s.AgentTree+"/.", tree).CombinedOutput(); err != nil {
			return fail("infra", fmt.Errorf("agent tree: %v: %s", err, out))
		}
	} else if len(s.AgentPatch) > 0 {
		pp := filepath.Join(s.Dir, "agent.patch")
		if err := os.WriteFile(pp, s.AgentPatch, 0o600); err != nil {
			return fail("infra", err)
		}
		cmd := exec.Command("patch", "-p1", "-s", "-d", tree, "-i", pp)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fail("infra", fmt.Errorf("agent patch does not apply: %v: %s", err, out))
		}
	} else if err := overlayChanges(s, tree); err != nil {
		return fail("infra", err)
	}
	// the agent's change, for the record
	patch, _ := exec.Command("diff", "-ruN", s.RepoDir, tree).Output() // exit 1 means "differs"
	patch = []byte(strings.ReplaceAll(string(patch), s.RepoDir, "a"))
	files := changedFiles(s.RepoDir, tree)
	v.ChangedFiles = files
	sum := sha256.Sum256(patch)
	v.PatchSHA256, v.PatchBytes = hex.EncodeToString(sum[:]), len(patch)
	_ = os.WriteFile(filepath.Join(s.Dir, "model.patch"), patch, 0o644)

	extra := []sandbox.ExtraFile{{Path: scriptGuestPath, Data: script, Mode: 0o555, RootOwned: true}}
	out, err := runInVM(ctx, s, tree, []string{"/bin/bash", scriptGuestPath}, extra)
	v.WallS = out.WallS
	v.OutputTail = tail(out.Lines, 4096)
	if err != nil {
		return fail("infra", err)
	}
	if out.Exit.Reason == "timeout" || out.Exit.Reason == "idle" {
		v.Status, v.Cause, v.ExitCode = "unresolved", "verify_timeout", out.Exit.Code
		return v, nil
	}
	j := Judge(s.Tests, ParseScriptOutcomes(out.Lines), len(files) == 0)
	j.ChangedFiles, j.PatchSHA256, j.PatchBytes = v.ChangedFiles, v.PatchSHA256, v.PatchBytes
	j.ExitCode, j.WallS, j.OutputTail, j.Attempts, j.CanaryOK = out.Exit.Code, v.WallS, v.OutputTail, 1, true
	return &j, nil
}

// changedFiles lists regular files that differ between two trees (added, removed
// or modified), ignoring noise, as paths relative to the root.
func changedFiles(a, b string) []string {
	seen := map[string]bool{}
	walk := func(root string) map[string][]byte {
		m := map[string][]byte{}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if isNoise(filepath.ToSlash(rel)) {
				return nil
			}
			if bs, err := os.ReadFile(p); err == nil {
				m[filepath.ToSlash(rel)] = bs
			}
			return nil
		})
		return m
	}
	am, bm := walk(a), walk(b)
	var out []string
	for k, bv := range bm {
		if av, ok := am[k]; !ok || string(av) != string(bv) {
			seen[k] = true
		}
	}
	for k := range am {
		if _, ok := bm[k]; !ok {
			seen[k] = true
		}
	}
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}
