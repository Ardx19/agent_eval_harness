package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentevalharness/internal/agents"
	"agentevalharness/internal/config"
)

// fakeRun replaces the real trial: it records how many ran at once and returns a
// canned summary, so the pool's scheduling can be checked without microVMs.
func fakeRun(cost float64, status string, delay time.Duration) (func(context.Context, Options, Task, int, int, []agents.Skill, string, string, bool, string, *Summary) TrialSummary, *int32, *int32) {
	var cur, peak int32
	return func(_ context.Context, _ Options, task Task, trial, attempt int, _ []agents.Skill, _, _ string, _ bool, _ string, _ *Summary) TrialSummary {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(delay)
		atomic.AddInt32(&cur, -1)
		return TrialSummary{TaskID: task.ID, Trial: trial, Status: status, CostUSD: cost, TracePath: "x", Flags: []string{}}
	}, &cur, &peak
}

func poolOpts(t *testing.T, workers, tasks int, budget float64) (Options, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	var lines []byte
	for i := 0; i < tasks; i++ {
		lines = append(lines, []byte(fmt.Sprintf(`{"id":"t%02d","prompt":"p"}`+"\n", i))...)
	}
	prompts := filepath.Join(dir, "prompts.jsonl")
	if err := os.WriteFile(prompts, lines, 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	return Options{Profile: &config.Profile{Agent: "pi", Provider: "google", Model: "m", Concurrency: workers, BudgetUSD: budget}, PromptsPath: prompts,
		OutDir: dir, Root: dir, N: 1, APIKey: "k", BudgetUSD: budget, Log: &log}, &log
}

func withFake(t *testing.T, f func(context.Context, Options, Task, int, int, []agents.Skill, string, string, bool, string, *Summary) TrialSummary) {
	old := runOneFn
	runOneFn = f
	t.Cleanup(func() { runOneFn = old })
}

func TestPoolRunsAtMostConcurrencyAtOnceAndKeepsOrder(t *testing.T) {
	f, _, peak := fakeRun(0.01, "ok", 40*time.Millisecond)
	withFake(t, f)
	o, _ := poolOpts(t, 3, 9, 100)
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(peak); got < 2 || got > 3 {
		t.Fatalf("peak concurrency %d, want 2 or 3", got)
	}
	if len(sum.Trials) != 9 {
		t.Fatalf("%d trials", len(sum.Trials))
	}
	for i, tr := range sum.Trials {
		if tr.TaskID != fmt.Sprintf("t%02d", i) {
			t.Fatalf("summary must stay in task order, got %s at %d", tr.TaskID, i)
		}
	}
	if sum.TotalCost < 0.089 || sum.TotalCost > 0.091 {
		t.Fatalf("cost %v", sum.TotalCost)
	}
}

func TestPoolDefaultsToOneAtATime(t *testing.T) {
	f, _, peak := fakeRun(0, "ok", 15*time.Millisecond)
	withFake(t, f)
	o, _ := poolOpts(t, 0, 4, 100)
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(peak); got != 1 {
		t.Fatalf("peak %d", got)
	}
}

func TestPoolStopsStartingTrialsOverBudget(t *testing.T) {
	f, _, _ := fakeRun(0.6, "ok", 5*time.Millisecond)
	withFake(t, f)
	o, log := poolOpts(t, 1, 6, 1.0)
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 2 || sum.Stopped == "" || !bytes.Contains(log.Bytes(), []byte("session budget")) {
		t.Fatalf("trials=%d stopped=%q", len(sum.Trials), sum.Stopped)
	}
}

func TestPoolRetriesInfrastructureFailures(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	withFake(t, func(_ context.Context, _ Options, task Task, trial, attempt int, _ []agents.Skill, _, _ string, _ bool, _ string, _ *Summary) TrialSummary {
		mu.Lock()
		calls[task.ID]++
		mu.Unlock()
		if attempt == 0 {
			return TrialSummary{TaskID: task.ID, Trial: trial, Status: "infra_error", TracePath: "x", Flags: []string{}}
		}
		return TrialSummary{TaskID: task.ID, Trial: trial, Status: "ok", TracePath: "x", Flags: []string{}}
	})
	o, _ := poolOpts(t, 2, 3, 100)
	o.Retries = 1
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range sum.Trials {
		if tr.Status != "ok" || tr.Attempts != 2 {
			t.Fatalf("%+v", tr)
		}
	}
	for id, n := range calls {
		if n != 2 {
			t.Fatalf("%s ran %d times", id, n)
		}
	}
}

// When the model API refuses a trial, the run pauses before starting anything else,
// each refusal in a row lengthens the pause, and a trial that gets answers resets it.
func TestRefusedTrialsPauseTheRun(t *testing.T) {
	oldFirst, oldMax := breakerFirst, breakerMax
	breakerFirst, breakerMax = 150*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { breakerFirst, breakerMax = oldFirst, oldMax })

	var mu sync.Mutex
	var starts []time.Time
	n := 0
	withFake(t, func(_ context.Context, _ Options, task Task, trial, attempt int, _ []agents.Skill, _, _ string, _ bool, _ string, _ *Summary) TrialSummary {
		mu.Lock()
		starts = append(starts, time.Now())
		n++
		call := n
		mu.Unlock()
		if call <= 2 { // the first two attempts are refused
			return TrialSummary{TaskID: task.ID, Trial: trial, Status: "infra_error", Reason: "upstream", TracePath: "x", Flags: []string{}}
		}
		return TrialSummary{TaskID: task.ID, Trial: trial, Status: "ok", TracePath: "x", Flags: []string{}}
	})
	o, log := poolOpts(t, 1, 2, 100)
	o.Retries = 3
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 4 { // task 1: refused, refused, ok; task 2: ok
		t.Fatalf("%d attempts", len(starts))
	}
	if gap := starts[1].Sub(starts[0]); gap < 140*time.Millisecond {
		t.Fatalf("the retry after a refusal must wait for the pause, started after %v", gap)
	}
	if gap := starts[2].Sub(starts[1]); gap < 290*time.Millisecond {
		t.Fatalf("a second refusal in a row must double the pause, got %v", gap)
	}
	if gap := starts[3].Sub(starts[2]); gap > 100*time.Millisecond {
		t.Fatalf("a trial that got answers must clear the pause, next trial waited %v", gap)
	}
	if sum.Trials[0].Status != "ok" || sum.Trials[0].Attempts != 3 || !bytes.Contains(log.Bytes(), []byte("refused")) {
		t.Fatalf("%+v\n%s", sum.Trials[0], log.String())
	}
}

// refusingRun answers each call from pattern: true means the model API refused that
// attempt, false means the trial got answers. It counts calls.
func refusingRun(t *testing.T, pattern []bool, calls *int) {
	t.Helper()
	var mu sync.Mutex
	withFake(t, func(_ context.Context, _ Options, task Task, trial, attempt int, _ []agents.Skill, _, _ string, _ bool, _ string, _ *Summary) TrialSummary {
		mu.Lock()
		i := *calls
		*calls++
		mu.Unlock()
		refuse := true // past the pattern, every call is refused
		if i < len(pattern) {
			refuse = pattern[i]
		}
		if refuse {
			return TrialSummary{TaskID: task.ID, Trial: trial, Status: "infra_error", Reason: "upstream", TracePath: "x", Flags: []string{}}
		}
		return TrialSummary{TaskID: task.ID, Trial: trial, Status: "ok", TracePath: "x", Flags: []string{}}
	})
}

// shortBreaker keeps the cooldowns short so refusal tests run quickly.
func shortBreaker(t *testing.T) {
	oldFirst, oldMax := breakerFirst, breakerMax
	breakerFirst, breakerMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { breakerFirst, breakerMax = oldFirst, oldMax })
}

// max_refusals stops the run once that many refusals come in a row: no trial starts
// after the limit, and the stop reason is recorded.
func TestRefusalLimitStopsTheRun(t *testing.T) {
	shortBreaker(t)
	calls := 0
	refusingRun(t, nil, &calls) // every call refused
	o, log := poolOpts(t, 1, 10, 100)
	o.Profile.Limits.MaxRefusals = 3
	o.Retries = 0
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 3 || calls != 3 {
		t.Fatalf("trials=%d calls=%d, want 3 and 3\n%s", len(sum.Trials), calls, log.String())
	}
	if !strings.HasPrefix(sum.Stopped, RefusalStop) {
		t.Fatalf("stopped=%q", sum.Stopped)
	}
	if !bytes.Contains(log.Bytes(), []byte(RefusalStop)) {
		t.Fatalf("the log must say why the run stopped:\n%s", log.String())
	}
}

// An answered trial resets the count: refused, refused, ok, refused, refused does not
// reach a limit of 3.
func TestRefusalLimitIsResetByAnswers(t *testing.T) {
	shortBreaker(t)
	calls := 0
	refusingRun(t, []bool{true, true, false, true, true}, &calls)
	o, _ := poolOpts(t, 1, 5, 100)
	o.Profile.Limits.MaxRefusals = 3
	o.Retries = 0
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 5 || sum.Stopped != "" {
		t.Fatalf("trials=%d stopped=%q, want all 5 and no stop", len(sum.Trials), sum.Stopped)
	}
}

// The default (0) never stops on refusals: the breaker pauses and the run finishes.
func TestRefusalLimitOffByDefault(t *testing.T) {
	shortBreaker(t)
	calls := 0
	refusingRun(t, nil, &calls)
	o, _ := poolOpts(t, 1, 4, 100)
	o.Retries = 0
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 4 || sum.Stopped != "" {
		t.Fatalf("trials=%d stopped=%q, want 4 and no stop", len(sum.Trials), sum.Stopped)
	}
}

// Retries count toward the limit, and a stop ends the retry loop too: with a limit of 2
// and retries allowed, a refused trial is tried exactly twice.
func TestRefusalLimitAppliesInsideRetries(t *testing.T) {
	shortBreaker(t)
	calls := 0
	refusingRun(t, nil, &calls)
	o, _ := poolOpts(t, 1, 1, 100)
	o.Profile.Limits.MaxRefusals = 2
	o.Retries = 5
	sum, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.HasPrefix(sum.Stopped, RefusalStop) {
		t.Fatalf("calls=%d stopped=%q, want 2 calls and a refusal stop", calls, sum.Stopped)
	}
}
