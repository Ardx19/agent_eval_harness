#!/usr/bin/env python3
"""Deterministic offline grader for recorded agent runs on the Covaln retrosynthesis tool.

    python grade_retro.py --data data/ --spec prompts/spec.jsonl --out out/ [--labels labels/reference_labels.jsonl]

Reads trace.json + blobs/ per trial, applies the per-prompt rules in the spec (SPEC.md section G), the decision
procedure (section D) and writes rubric_results.jsonl, report.md, review_queue.jsonl and the grade_*.csv files.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from grader.decide import grade_trial
from grader.model import discover_trials, load_trial
from grader.outputs import anomalies, write_all
from grader.textutil import HAVE_RDKIT


def load_specs(path: Path) -> dict:
    specs = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            d = json.loads(line)
            specs[d["id"]] = d
    return specs


def load_labels(path: Path) -> dict:
    labels = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        d = json.loads(line)
        task = d.get("task") or d.get("task_id") or d.get("id")
        trial = int(d.get("trial", 1))
        run = d.get("run") or d.get("run_id")
        out = d.get("outcome") or d.get("label") or d.get("verdict")
        rec = {"outcome": str(out).upper() if out else ""}
        labels[(run, task, trial)] = rec
        labels.setdefault((task, trial), rec)
    return labels


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--data", required=True, type=Path)
    ap.add_argument("--spec", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--labels", type=Path, default=None)
    ap.add_argument("--degrade", choices=["best-effort", "surface-only"], default="best-effort",
                    help="trials without host call records: best-effort evaluates evidence rules from the trajectory (confidence low); "
                         "surface-only skips the evidence layer, as SPEC section H states literally")
    args = ap.parse_args(argv)

    specs = load_specs(args.spec)
    results, trials = [], {}
    dirs = discover_trials(args.data)
    if not dirs:
        print(f"no trace.json found under {args.data}", file=sys.stderr)
        return 2
    for d in dirs:
        try:
            t = load_trial(d)
        except Exception as e:  # a broken trace must not stop the run; record it as an infrastructure failure
            print(f"warning: cannot load {d}: {type(e).__name__}: {e}", file=sys.stderr)
            continue
        spec = specs.get(t.task)
        r = grade_trial(t, spec, degrade=args.degrade)
        r["category"] = (spec or {}).get("category", "unspecced")
        r["anomalies"] = anomalies(t)
        results.append(r)
        trials[(t.run, t.task, t.trial)] = t
    results.sort(key=lambda r: (r["run"], r["task"], r["trial"], r["model"]))
    labels = load_labels(args.labels) if args.labels and args.labels.exists() else None
    write_all(args.out, results, trials, specs, {"rdkit": HAVE_RDKIT, "degrade": args.degrade}, labels)
    print(f"graded {len(results)} trial(s) -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
