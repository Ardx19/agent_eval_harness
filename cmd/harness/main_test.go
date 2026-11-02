package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func exec(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(args, &o, &e)
	return code, o.String(), e.String()
}

func fixture(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fixtures/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNoArgsPrintsUsage(t *testing.T) {
	code, _, errs := exec()
	if code != 2 || !strings.Contains(errs, "usage: harness") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errs := exec("frobnicate")
	if code != 2 || !strings.Contains(errs, `unknown command "frobnicate"`) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRunNeedsAReadablePromptsFile(t *testing.T) {
	code, _, errs := exec("run", "--profile", fixture(t), "--prompts", "/nonexistent/prompts.jsonl", "--skip-preflight", "--root", "/nonexistent")
	if code != 1 || !strings.Contains(errs, "no such file") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRunMissingProfileFlag(t *testing.T) {
	code, _, errs := exec("run")
	if code != 2 || !strings.Contains(errs, "--profile is required") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRunMissingProfileFile(t *testing.T) {
	code, _, errs := exec("run", "--profile", "/nonexistent/profile.yaml")
	if code != 1 || !strings.Contains(errs, "read profile") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRunRefusesAnUnreadyMachine(t *testing.T) {
	code, _, errs := exec("run", "--profile", fixture(t), "--prompts", "/nonexistent.jsonl", "--root", "/nonexistent")
	if code != 1 || !strings.Contains(errs, "not ready") {
		t.Fatalf("a missing firecracker must stop the run before any trial: code=%d err=%q", code, errs)
	}
}

func TestGradeNeedsARunDirectory(t *testing.T) {
	if code, _, errs := exec("grade"); code != 2 || !strings.Contains(errs, "--run is required") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if code, _, errs := exec("grade", "--run", "/nonexistent"); code != 1 || !strings.Contains(errs, "no such file") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

// The split must be named: scoring held-out by default would spend the one held-out scoring.
func TestEvalDetectorsNeedsASplit(t *testing.T) {
	for _, args := range [][]string{
		{"eval-detectors", "--pack", "/nonexistent"},
		{"eval-detectors", "--pack", "/nonexistent", "--split", "all"},
	} {
		if code, _, errs := exec(args...); code != 2 || !strings.Contains(errs, "--split is required") {
			t.Fatalf("%v: code=%d err=%q", args, code, errs)
		}
	}
}

// A run exits nonzero for a problem trial or a refusal stop; a clean run and other early
// stops (budget, interrupt) exit zero.
func TestExitStatus(t *testing.T) {
	cases := []struct {
		name    string
		problem int
		stopped string
		want    int
	}{
		{"clean run", 0, "", 0},
		{"problem trial", 1, "", 1},
		{"refusal stop", 0, "stopped early: model API refusing (3 refusals in a row)", 1},
		{"budget stop, clean", 0, "session budget of $1.00 reached before t01 trial 1", 0},
		{"interrupted, clean", 0, "interrupted", 0},
	}
	for _, c := range cases {
		if got := exitStatus(c.problem, c.stopped); got != c.want {
			t.Errorf("%s: exitStatus = %d, want %d", c.name, got, c.want)
		}
	}
}
