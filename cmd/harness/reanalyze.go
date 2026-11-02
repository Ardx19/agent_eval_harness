package main

import (
	"flag"
	"fmt"
	"io"

	"agentevalharness/internal/runner"
)

// cmdReanalyze re-runs the provenance checks over stored runs. It never runs an
// agent and never spends money.
func cmdReanalyze(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("reanalyze", flag.ContinueOnError)
	fs.SetOutput(errw)
	run := fs.String("run", "", "a run directory (e.g. runs/r-20261003-155527); required")
	root := fs.String("root", ".", "repository root (for skills/)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *run == "" {
		fmt.Fprintln(errw, "reanalyze: --run is required")
		return 2
	}
	results, err := runner.ReanalyzeRun(*root, *run)
	if err != nil {
		fmt.Fprintf(errw, "reanalyze: %v\n", err)
		return 1
	}
	changed := 0
	for _, r := range results {
		fmt.Fprintln(out, r.Describe())
		if r.Error == "" && fmt.Sprint(r.Before) != fmt.Sprint(r.After) {
			changed++
		}
	}
	fmt.Fprintf(out, "\n%d trial(s) examined, %d with changed findings; originals untouched, results in trace.reanalyzed.json\n", len(results), changed)
	return 0
}
