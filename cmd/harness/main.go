// Command harness runs agents in disposable microVMs and records provenance-checked traces.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"agentevalharness/internal/config"
	"agentevalharness/internal/sandbox"
)

const usage = `usage: harness <command> [flags]

commands:
  doctor     check that this machine can run trials
  run        run prompts against an agent and write a trace per trial
  reanalyze  re-run the provenance checks over stored runs (free)
  validate   gate a coding-task suite: gold must resolve, empty must not
  review-pack     write a blind review pack of traces for labelling
  eval-detectors  score the fabrication detectors against reviewer labels
  grade      score a stored run from its traces (resolve rate, flags, cost)
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errw, usage)
		return 2
	}
	switch args[0] {
	case "doctor":
		return doctor(args[1:], out, errw)
	case "run":
		return cmdRun(args[1:], out, errw)
	case "reanalyze":
		return cmdReanalyze(args[1:], out, errw)
	case "review-pack":
		return cmdReviewPack(args[1:], out, errw)
	case "eval-detectors":
		return cmdEvalDetectors(args[1:], out, errw)
	case "validate":
		return cmdValidate(args[1:], out, errw)
	case "grade":
		return cmdGrade(args[1:], out, errw)
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return 0
	default:
		fmt.Fprintf(errw, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// stub parses the flags each future command will take, validates the profile
// when one is required, and reports that the command is not built yet.
func stub(name string, args []string, out, errw io.Writer, needProfile bool) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errw)
	profile := fs.String("profile", "", "path to profile.yaml")
	fs.String("prompts", "prompts.jsonl", "task prompts file")
	fs.String("out", "runs", "output directory")
	fs.Int("n", 1, "trials per task")
	fs.Int64("seed", 1, "run seed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if needProfile || *profile != "" {
		if *profile == "" {
			fmt.Fprintf(errw, "%s: --profile is required\n", name)
			return 2
		}
		if _, err := config.Load(*profile); err != nil {
			fmt.Fprintf(errw, "%s: %v\n", name, err)
			return 1
		}
	}
	fmt.Fprintf(errw, "%s: not implemented yet\n", name)
	return 3
}

func doctor(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(errw)
	profilePath := fs.String("profile", "", "optional profile.yaml (provides provider, model, concurrency)")
	artifacts := fs.String("artifacts", "spike/firecracker", "directory with firecracker and the kernel")
	image := fs.String("image", "images/build/rootfs.ext4", "guest base image")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := sandbox.Options{
		ArtifactsDir: *artifacts,
		BaseImage:    *image,
		Concurrency:  2,
		VMMemMiB:     1536,
		Provider:     "google",
		Model:        "gemini-3.5-flash-lite",
	}
	if *profilePath != "" {
		p, err := config.Load(*profilePath)
		if err != nil {
			fmt.Fprintf(errw, "doctor: %v\n", err)
			return 1
		}
		opts.Concurrency, opts.VMMemMiB = p.Concurrency, p.VMMemMiB
		opts.Provider, opts.Model = p.Provider, p.Model
	}
	report := sandbox.RunPreflight(context.Background(), opts)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		printReport(out, report)
	}
	if report.Failed() {
		return 1
	}
	return 0
}

func printReport(out io.Writer, r sandbox.Report) {
	fmt.Fprintf(out, "host kernel: %s\n\n", r.HostKernel)
	tag := map[sandbox.Status]string{sandbox.OK: " ok ", sandbox.Warn: "warn", sandbox.Fail: "FAIL"}
	for _, c := range r.Checks {
		fmt.Fprintf(out, "[%s] %-22s %s\n", tag[c.Status], c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(out, "       fix: %s\n", c.Fix)
		}
	}
	if r.Failed() {
		fmt.Fprintln(out, "\nresult: NOT READY")
	} else {
		fmt.Fprintln(out, "\nresult: ready")
	}
}
