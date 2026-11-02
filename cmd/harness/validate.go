package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"path/filepath"
	"syscall"

	"agentevalharness/internal/config"
	"agentevalharness/internal/runner"
)

// cmdValidate gates a coding-task suite: each task's gold patch must resolve it
// every time, and doing nothing must not. Results are written next to the hidden
// tests and `harness run` skips tasks that failed.
func cmdValidate(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(errw)
	profilePath := fs.String("profile", "", "path to profile.yaml (required)")
	prompts := fs.String("prompts", "prompts.jsonl", "task prompts, one JSON object per line")
	outDir := fs.String("out", "runs", "directory for validation scratch files")
	runs := fs.Int("gold-runs", 3, "times each gold patch is verified")
	root := fs.String("root", ".", "repository root (images/, spike/, envs/)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profilePath == "" {
		fmt.Fprintln(errw, "validate: --profile is required")
		return 2
	}
	prof, err := config.Load(*profilePath)
	if err != nil {
		fmt.Fprintf(errw, "validate: %v\n", err)
		return 1
	}
	o := runner.ValidateOptions{GoldRuns: *runs, Options: runner.Options{
		Profile: prof, PromptsPath: *prompts, OutDir: *outDir, Root: *root, Log: out,
		FirecrackerBin: filepath.Join(*root, "spike/firecracker/firecracker"),
		Kernel:         filepath.Join(*root, "spike/firecracker/vmlinux-6.1.186"),
		BaseImage:      filepath.Join(*root, "images/build/rootfs.ext4"),
		Entries:        filepath.Join(*root, "images/build/entries.jsonl"),
	}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := runner.Validate(ctx, o)
	if err != nil {
		fmt.Fprintf(errw, "validate: %v\n", err)
		return 1
	}
	bad := 0
	for _, v := range res {
		if v.Status != "ok" {
			bad++
		}
	}
	fmt.Fprintf(out, "\n%d task(s) validated, %d invalid\n", len(res), bad)
	if bad > 0 {
		return 1
	}
	return 0
}
