package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agentevalharness/internal/grade"
)

// cmdGrade scores a stored run from its traces alone and writes grade.json and
// report.md beside them. It starts no VM and reads no model output but what the
// traces already hold.
func cmdGrade(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	fs.SetOutput(errw)
	run := fs.String("run", "", "run directory (runs/r-...) holding summary.json (required)")
	expPath := fs.String("expectations", "", "JSONL of per-task checks (id \"*\" holds defaults); host-only, never shown to the agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *run == "" {
		fmt.Fprintln(errw, "grade: --run is required")
		return 2
	}
	var exp map[string]grade.Expectation
	if *expPath != "" {
		var err error
		if exp, err = grade.LoadExpectations(*expPath); err != nil {
			fmt.Fprintf(errw, "grade: %v\n", err)
			return 1
		}
	}
	rep, err := grade.RunWith(*run, exp)
	if err != nil {
		fmt.Fprintf(errw, "grade: %v\n", err)
		return 1
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(*run, "grade.json"), append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(errw, "grade: %v\n", err)
		return 1
	}
	md := rep.Markdown()
	if err := os.WriteFile(filepath.Join(*run, "report.md"), []byte(md), 0o644); err != nil {
		fmt.Fprintf(errw, "grade: %v\n", err)
		return 1
	}
	fmt.Fprint(out, md)
	return 0
}
