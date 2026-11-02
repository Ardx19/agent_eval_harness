package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"agentevalharness/internal/config"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/runner"
	"agentevalharness/internal/sandbox"
)

// cmdRun runs every task in a prompts file against the agent, one microVM trial
// each, and writes a trace per trial. It reads prompts only: the grader's
// expectations are a separate stage and never loaded here.
func cmdRun(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(errw)
	profilePath := fs.String("profile", "", "path to profile.yaml (required)")
	prompts := fs.String("prompts", "prompts.jsonl", "task prompts, one JSON object per line")
	outDir := fs.String("out", "runs", "output directory")
	n := fs.Int("n", 1, "trials per task")
	seed := fs.Int64("seed", 1, "run seed")
	budget := fs.Float64("budget-usd", 0, "stop starting new trials once this much has been spent (0 = the profile's budget)")
	retries := fs.Int("retries", 1, "extra attempts for a trial that failed for infrastructure reasons (the failed attempt's trace is kept)")
	root := fs.String("root", ".", "repository root (skills/, images/, spike/)")
	fc := fs.String("firecracker", "", "firecracker binary (default: <root>/spike/firecracker/firecracker)")
	kernel := fs.String("kernel", "", "guest kernel (default: <root>/spike/firecracker/vmlinux-6.1.186)")
	image := fs.String("image", "", "base image (default: <root>/images/build/rootfs.ext4)")
	skipPreflight := fs.Bool("skip-preflight", false, "do not run the host readiness checks first")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profilePath == "" {
		fmt.Fprintln(errw, "run: --profile is required")
		return 2
	}
	prof, err := config.Load(*profilePath)
	if err != nil {
		fmt.Fprintf(errw, "run: %v\n", err)
		return 1
	}
	pick := func(v, rel string) string {
		if v != "" {
			return v
		}
		return filepath.Join(*root, rel)
	}
	opts := runner.Options{
		Profile: prof, PromptsPath: *prompts, OutDir: *outDir, Root: *root, N: *n, Seed: *seed, BudgetUSD: *budget, Retries: *retries,
		FirecrackerBin: pick(*fc, "spike/firecracker/firecracker"),
		Kernel:         pick(*kernel, "spike/firecracker/vmlinux-6.1.186"),
		BaseImage:      pick(*image, "images/build/rootfs.ext4"),
		Entries:        pick("", "images/build/entries.jsonl"),
		ImageManifest:  pick("", "images/build/manifest.json"),
		Upstream:       os.Getenv("EVH_UPSTREAM"), // tests point this at a fake model
		APIKey:         os.Getenv("EVH_API_KEY"),
		Log:            out,
	}
	if !*skipPreflight {
		pre := sandbox.Options{
			ArtifactsDir: filepath.Dir(opts.FirecrackerBin), BaseImage: opts.BaseImage, Concurrency: prof.Concurrency, VMMemMiB: prof.VMMemMiB,
			Provider: prof.Provider, Model: prof.Model,
		}
		if prof.API == "openai" {
			pre.KeyCheck = func() error { _, err := proxy.FetchOpencodeKey(prof.Provider); return err }
		}
		rep := sandbox.RunPreflight(context.Background(), pre)
		if rep.Failed() {
			printReport(errw, rep)
			fmt.Fprintln(errw, "run: this machine is not ready; fix the failures above (or --skip-preflight)")
			return 1
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	sum, err := runner.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(errw, "run: %v\n", err)
		return 1
	}
	var ok, bad int
	for _, t := range sum.Trials {
		if t.Error != "" || t.Status == "infra_error" {
			bad++
		} else {
			ok++
		}
	}
	fmt.Fprintf(out, "\n%d trial(s) recorded, %d with problems, total cost $%.4f\ntraces: %s\n", ok+bad, bad, sum.TotalCost, sum.Dir)
	if sum.Stopped != "" {
		fmt.Fprintf(out, "stopped early: %s\n", sum.Stopped)
	}
	return exitStatus(bad, sum.Stopped)
}

// exitStatus is the exit code of a run: nonzero when a trial had a problem or the run
// was stopped because the model API kept refusing. Other early stops (budget, interrupt)
// keep exiting zero when every recorded trial is clean.
func exitStatus(problems int, stopped string) int {
	if problems > 0 || strings.HasPrefix(stopped, runner.RefusalStop) {
		return 1
	}
	return 0
}
