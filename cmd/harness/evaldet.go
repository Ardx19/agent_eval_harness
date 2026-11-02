package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agentevalharness/internal/evaldet"
)

// cmdReviewPack writes a blind review pack for the given run directories.
func cmdReviewPack(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("review-pack", flag.ContinueOnError)
	fs.SetOutput(errw)
	outDir := fs.String("out", "review-pack", "directory for the pack (one .md per trial plus key.jsonl)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(errw, "review-pack: give one or more run directories (runs/r-...)")
		return 2
	}
	n, err := evaldet.BuildPack(fs.Args(), *outDir)
	if err != nil {
		fmt.Fprintf(errw, "review-pack: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "%d trace(s) written to %s\nGive the .md files and README.md to the reviewer; keep key.jsonl. Labels go in labels.jsonl.\n", n, *outDir)
	return 0
}

// cmdEvalDetectors scores the current detectors against reviewer labels.
func cmdEvalDetectors(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("eval-detectors", flag.ContinueOnError)
	fs.SetOutput(errw)
	pack := fs.String("pack", "", "review pack directory holding key.jsonl (required)")
	labels := fs.String("labels", "", "labels.jsonl (default: <pack>/labels.jsonl)")
	split := fs.String("split", "", "which labels to score: dev or heldout (required)")
	root := fs.String("root", ".", "repository root")
	report := fs.String("report", "", "also write the markdown report here")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pack == "" {
		fmt.Fprintln(errw, "eval-detectors: --pack is required")
		return 2
	}
	if *split != "dev" && *split != "heldout" {
		fmt.Fprintln(errw, "eval-detectors: --split is required: dev or heldout")
		return 2
	}
	if *labels == "" {
		*labels = filepath.Join(*pack, "labels.jsonl")
	}
	key, err := evaldet.LoadKey(filepath.Join(*pack, "key.jsonl"))
	if err != nil {
		fmt.Fprintf(errw, "eval-detectors: %v\n", err)
		return 1
	}
	ls, err := evaldet.LoadLabels(*labels)
	if err != nil {
		fmt.Fprintf(errw, "eval-detectors: %v\n", err)
		return 1
	}
	if *split == "heldout" {
		fmt.Fprintln(errw, "note: the held-out split is meant to be scored once, after detector fixes are frozen")
	}
	rep, err := evaldet.Evaluate(*root, key, ls, *split)
	if err != nil {
		fmt.Fprintf(errw, "eval-detectors: %v\n", err)
		return 1
	}
	md := rep.Markdown()
	fmt.Fprint(out, md)
	if *report != "" {
		if err := os.WriteFile(*report, []byte(md), 0o644); err != nil {
			fmt.Fprintf(errw, "eval-detectors: %v\n", err)
			return 1
		}
	}
	return 0
}
