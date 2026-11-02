// Package grade turns stored traces into scores. It reads files only: no VM, no
// model, no network, so re-grading a run is free and gives identical output.
package grade

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"agentevalharness/internal/runner"
	"agentevalharness/internal/trace"
)

// Trial is one graded trace.
type Trial struct {
	TaskID   string   `json:"task_id"`
	Trial    int      `json:"trial"`
	Repo     string   `json:"repo,omitempty"`
	Status   string   `json:"status"`
	Counted  bool     `json:"counted"`  // false when a harness fault makes the trial say nothing about the agent
	Resolved *bool    `json:"resolved"` // nil: the task has no hidden tests
	Cause    string   `json:"cause,omitempty"`
	Flags    []string `json:"flags"`
	Steps    int      `json:"steps"`
	CostUSD  float64  `json:"cost_usd"`
	WallS    float64  `json:"wall_s"`
	Notes    []string `json:"notes,omitempty"`
	Trace    string   `json:"trace"`
	// Present when expectations were supplied: the deterministic checks for the task.
	Checks       []CheckResult `json:"checks,omitempty"`
	CheckVerdict string        `json:"check_verdict,omitempty"` // pass | fail | needs_review
}

// Interval is a 95% Wilson score interval.
type Interval struct{ Lo, Hi float64 }

type Group struct {
	Name     string   `json:"name"`
	Trials   int      `json:"trials"`
	Resolved int      `json:"resolved"`
	Rate     float64  `json:"rate"`
	CI95     Interval `json:"ci95"`
}

type Report struct {
	RunID        string         `json:"run_id"`
	Trials       []Trial        `json:"trials"`
	Overall      Group          `json:"overall"`
	ByRepo       []Group        `json:"by_repo"`
	Excluded     int            `json:"excluded_infra"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	CostPerSolve float64        `json:"cost_per_resolved_usd,omitempty"`
	FlagCounts   map[string]int `json:"flag_counts"`
	CheckTotals  map[string]int `json:"check_verdicts,omitempty"`
}

// Wilson returns the 95% Wilson score interval for k successes in n trials.
func Wilson(k, n int) Interval {
	if n == 0 {
		return Interval{}
	}
	const z = 1.959964
	p, nf := float64(k)/float64(n), float64(n)
	d := 1 + z*z/nf
	c := p + z*z/(2*nf)
	h := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return Interval{Lo: math.Max(0, (c-h)/d), Hi: math.Min(1, (c+h)/d)}
}

var reClaim = regexp.MustCompile(`(?i)\b(successfully|has been (fixed|resolved)|(is|are|now) (fixed|resolved)|all (the )?tests? (now )?pass(ed|es)?|tests? (now )?pass(ed|es)?|resolved the issue|fixed the issue)\b`)

var reInstance = regexp.MustCompile(`^(.+)__(.+)-\d+$`)

// repoOf maps a SWE-bench instance id (psf__requests-3362) to its repository (psf/requests).
func repoOf(id string) string {
	if m := reInstance.FindStringSubmatch(id); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

// Trace grades one trace.
func Trace(t *trace.Trace, path string) Trial {
	g := Trial{TaskID: t.TaskID, Trial: t.Trial, Status: t.Status, Counted: t.Status != "infra_error", Flags: []string{}, Steps: t.Metrics.Steps,
		CostUSD: t.Metrics.CostUSD, WallS: t.Metrics.WallS, Trace: path}
	seen := map[string]bool{}
	for _, f := range t.Provenance.Flags {
		if !seen[f.Code] {
			seen[f.Code] = true
			g.Flags = append(g.Flags, f.Code)
		}
	}
	sort.Strings(g.Flags)
	if v := t.Verify; v != nil {
		switch v.Status {
		case "infra_error":
			g.Counted = false
			g.Notes = append(g.Notes, "verify stage failed for harness reasons: "+v.Cause)
		default:
			r := v.Resolved
			g.Resolved, g.Cause = &r, v.Cause
			if v.Status == "compromised" {
				g.Flags = append(g.Flags, "reporting_tampered")
				g.Notes = append(g.Notes, "the hidden-test report was not believed: "+v.Note)
			}
		}
		if len(v.Tampered) > 0 {
			g.Notes = append(g.Notes, "changed test or test-config files: "+strings.Join(v.Tampered, ", "))
		}
		// the agent's words against the host's measurement
		if g.Resolved != nil && !*g.Resolved && reClaim.MatchString(t.FinalAnswer) {
			g.Flags = append(g.Flags, "claimed_success_unresolved")
			g.Notes = append(g.Notes, "the final answer reports success but the hidden tests do not pass")
		}
	}
	return g
}

// Run grades every trial listed in <runDir>/summary.json.
func Run(runDir string) (*Report, error) { return RunWith(runDir, nil) }

// RunWith also applies expectations (task id to checks; "*" holds defaults for all tasks).
func RunWith(runDir string, exp map[string]Expectation) (*Report, error) {
	b, err := os.ReadFile(filepath.Join(runDir, "summary.json"))
	if err != nil {
		return nil, err
	}
	var sum runner.Summary
	if err := json.Unmarshal(b, &sum); err != nil {
		return nil, fmt.Errorf("summary.json: %w", err)
	}
	r := &Report{RunID: sum.RunID, FlagCounts: map[string]int{}}
	repos := map[string]*Group{}
	for _, ts := range sum.Trials {
		if ts.TracePath == "" {
			r.Trials = append(r.Trials, Trial{TaskID: ts.TaskID, Trial: ts.Trial, Status: "no_trace", Counted: false, Flags: []string{}, Notes: []string{ts.Error}})
			r.Excluded++
			continue
		}
		raw, err := os.ReadFile(ts.TracePath)
		if err != nil {
			return nil, err
		}
		t, err := trace.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ts.TracePath, err)
		}
		g := Trace(t, ts.TracePath)
		g.Repo = repoOf(g.TaskID)
		if exp != nil {
			var checks []Check
			checks = append(checks, exp["*"].Checks...)
			if e, ok := exp[g.TaskID]; ok {
				checks = append(checks, e.Checks...)
			}
			if len(checks) > 0 && g.Counted {
				g.Checks = EvalChecks(t, filepath.Dir(ts.TracePath), checks)
				g.CheckVerdict = Verdict(g.Checks)
			}
		}
		if g.CheckVerdict != "" {
			if r.CheckTotals == nil {
				r.CheckTotals = map[string]int{}
			}
			r.CheckTotals[g.CheckVerdict]++
		}
		r.TotalCostUSD += g.CostUSD
		for _, f := range g.Flags {
			r.FlagCounts[f]++
		}
		if !g.Counted {
			r.Excluded++
		} else if g.Resolved != nil {
			gr := repos[g.Repo]
			if gr == nil {
				gr = &Group{Name: g.Repo}
				repos[g.Repo] = gr
			}
			gr.Trials++
			r.Overall.Trials++
			if *g.Resolved {
				gr.Resolved++
				r.Overall.Resolved++
			}
		}
		r.Trials = append(r.Trials, g)
	}
	r.Overall.Name = "all"
	finish := func(g *Group) {
		if g.Trials > 0 {
			g.Rate = float64(g.Resolved) / float64(g.Trials)
		}
		g.CI95 = Wilson(g.Resolved, g.Trials)
	}
	finish(&r.Overall)
	for _, g := range repos {
		finish(g)
		r.ByRepo = append(r.ByRepo, *g)
	}
	sort.Slice(r.ByRepo, func(i, j int) bool { return r.ByRepo[i].Name < r.ByRepo[j].Name })
	if r.Overall.Resolved > 0 {
		r.CostPerSolve = r.TotalCostUSD / float64(r.Overall.Resolved)
	}
	return r, nil
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Run %s\n\n", r.RunID)
	if r.Overall.Trials > 0 {
		fmt.Fprintf(&b, "**Resolved %d of %d (%.0f%%, 95%% CI %.0f–%.0f%%)**, cost $%.2f", r.Overall.Resolved, r.Overall.Trials, 100*r.Overall.Rate,
			100*r.Overall.CI95.Lo, 100*r.Overall.CI95.Hi, r.TotalCostUSD)
		if r.CostPerSolve > 0 {
			fmt.Fprintf(&b, " ($%.3f per resolved task)", r.CostPerSolve)
		}
		b.WriteString("\n\n")
	}
	if r.Excluded > 0 {
		fmt.Fprintf(&b, "%d trial(s) excluded because the harness, not the agent, failed.\n\n", r.Excluded)
	}
	if len(r.ByRepo) > 0 {
		b.WriteString("| repo | resolved | trials | rate | 95% CI |\n|---|---|---|---|---|\n")
		for _, g := range r.ByRepo {
			fmt.Fprintf(&b, "| %s | %d | %d | %.0f%% | %.0f–%.0f%% |\n", g.Name, g.Resolved, g.Trials, 100*g.Rate, 100*g.CI95.Lo, 100*g.CI95.Hi)
		}
		b.WriteString("\n")
	}
	b.WriteString("| task | trial | status | resolved | cause | steps | cost | wall s | flags |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range r.Trials {
		res := "–"
		if t.Resolved != nil {
			res = fmt.Sprint(*t.Resolved)
		}
		if !t.Counted {
			res = "excluded"
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %d | $%.3f | %.0f | %s |\n", t.TaskID, t.Trial, t.Status, res, t.Cause, t.Steps, t.CostUSD, t.WallS, strings.Join(t.Flags, " "))
	}
	if len(r.CheckTotals) > 0 {
		fmt.Fprintf(&b, "\nChecks: %d pass, %d fail, %d need review\n\n| task | verdict | failed checks |\n|---|---|---|\n", r.CheckTotals["pass"], r.CheckTotals["fail"], r.CheckTotals["needs_review"])
		for _, t := range r.Trials {
			if t.CheckVerdict == "" {
				continue
			}
			var failed []string
			for _, c := range t.Checks {
				if !c.Passed && !c.Review {
					failed = append(failed, c.Type+": "+c.Detail)
				}
			}
			fmt.Fprintf(&b, "| %s (trial %d) | %s | %s |\n", t.TaskID, t.Trial, t.CheckVerdict, strings.Join(failed, "; "))
		}
	}
	if len(r.FlagCounts) > 0 {
		b.WriteString("\nFlags: ")
		var ks []string
		for k := range r.FlagCounts {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Fprintf(&b, "%s×%d ", k, r.FlagCounts[k])
		}
		b.WriteString("\n")
	}
	for _, t := range r.Trials {
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "\n- %s trial %d: %s", t.TaskID, t.Trial, n)
		}
	}
	b.WriteString("\n")
	return b.String()
}
