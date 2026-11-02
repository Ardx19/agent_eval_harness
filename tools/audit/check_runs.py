#!/usr/bin/env python3
"""Check stored run evidence: every trace decodes, every sha256 reference resolves to a
blob whose bytes hash to that reference, and summary.json agrees with the trace files.

Usage:
  python3 tools/audit/check_runs.py [--root DIR] [--harbor N] RUN_DIR [RUN_DIR ...]

--harbor N also runs Harbor's own ATIF validator on up to N traces, sampled round-robin
across (agent, model) groups, using tools/harbor-venv. Skipped with a note if the
validator is not installed.

--root is the repository root that summary.json trace_path values are relative to
(default: the current directory). Exit 0 when no problem is found, 1 otherwise.
Problems are printed one per line with the file they concern.
"""
import argparse
import hashlib
import json
import pathlib
import re
import subprocess
import sys
import tempfile
from collections import defaultdict

SCHEMA = "evalharness-2"
REF = re.compile(r"^sha256:([0-9a-f]{64})$")


def walk_refs(node, out):
    """Collect every string that is a sha256 reference, at any depth."""
    if isinstance(node, dict):
        for v in node.values():
            walk_refs(v, out)
    elif isinstance(node, list):
        for v in node:
            walk_refs(v, out)
    elif isinstance(node, str):
        m = REF.match(node)
        if m:
            out.add(m.group(1))
    return out


def check_trace(path, problems, stats):
    """Decode one trace.json and check its schema version and blob references."""
    try:
        trace = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as e:
        problems.append(f"{path}: does not decode ({e})")
        return
    stats["traces"] += 1
    if trace.get("schema_version") != SCHEMA:
        problems.append(f"{path}: schema_version is {trace.get('schema_version')!r}, want {SCHEMA!r}")
    blobs = path.parent / "blobs"
    for hexsum in sorted(walk_refs(trace, set())):
        stats["refs"] += 1
        blob = blobs / hexsum
        if not blob.is_file():
            problems.append(f"{path}: dangling reference sha256:{hexsum} (no {blob})")
            continue
        actual = hashlib.sha256(blob.read_bytes()).hexdigest()
        if actual != hexsum:
            problems.append(f"{path}: blob {blob} hashes to {actual}, referenced as {hexsum}")


def check_run(run_dir, root, problems, stats):
    summary_path = run_dir / "summary.json"
    listed = set()
    if summary_path.is_file():
        try:
            summary = json.loads(summary_path.read_text())
        except json.JSONDecodeError as e:
            problems.append(f"{summary_path}: does not decode ({e})")
            summary = {"trials": []}
        trials = summary.get("trials", [])
        with_trace = 0
        for t in trials:
            tp = t.get("trace_path") or ""
            if not tp:
                continue
            with_trace += 1
            p = pathlib.Path(tp)
            if not p.is_absolute():
                p = root / p
            if not p.is_file():
                problems.append(f"{summary_path}: trace_path {tp} does not exist")
                continue
            listed.add(p.resolve())
        stats["summary_trials"] += len(trials)
        stats["summary_with_trace"] += with_trace
    found = {p.resolve() for p in run_dir.rglob("trace.json")}
    for p in sorted(found):
        check_trace(p, problems, stats)
    if summary_path.is_file():
        extra = found - listed
        # trace.json files that summary.json does not point at: superseded retry attempts
        # are normal, so they are counted, not reported as problems.
        stats["unlisted_traces"] += len(extra)
        missing = listed - found
        for p in sorted(missing):
            problems.append(f"{summary_path}: listed trace {p} is not under this run")


HARBOR_PY = pathlib.Path(__file__).resolve().parents[1] / "harbor-venv" / "bin" / "python"


def harbor_sample(run_dirs, n, problems, notes):
    """Run Harbor's ATIF validator on up to n traces, round-robin over (agent, model)."""
    if not HARBOR_PY.is_file():
        notes.append("harbor validator not installed (tools/harbor-venv); ATIF check skipped")
        return 0
    groups = defaultdict(list)
    for run_dir in run_dirs:
        for p in sorted(run_dir.rglob("trace.json")):
            try:
                t = json.loads(p.read_text())
            except (OSError, json.JSONDecodeError):
                continue
            cfg = t.get("agent_config") or {}
            groups[(cfg.get("agent", "?"), cfg.get("model", "?"))].append((p, t))
    picked, keys = [], sorted(groups)
    i = 0
    while len(picked) < n and any(groups[k] for k in keys):
        k = keys[i % len(keys)]
        if groups[k]:
            picked.append(groups[k].pop(0))
        i += 1
    checked = 0
    with tempfile.TemporaryDirectory() as tmp:
        for p, t in picked:
            traj = pathlib.Path(tmp, "trajectory.json")
            traj.write_text(json.dumps(t.get("trajectory"), indent=1))
            r = subprocess.run([str(HARBOR_PY), "-m", "harbor.utils.trajectory_validator", "--no-validate-images", str(traj)],
                               capture_output=True, text=True)
            checked += 1
            if r.returncode != 0:
                problems.append(f"{p}: Harbor ATIF validator rejected the trajectory: {(r.stdout + r.stderr).strip()[:300]}")
    notes.append(f"harbor ATIF validator run on {checked} trace(s) across {len(keys)} agent/model group(s)")
    return checked


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", default=".", help="repository root for relative trace_path values")
    ap.add_argument("--harbor", type=int, default=0, metavar="N", help="also run Harbor's ATIF validator on up to N traces")
    ap.add_argument("runs", nargs="+", help="run directories to check")
    args = ap.parse_args(argv)
    root = pathlib.Path(args.root).resolve()
    problems, stats, notes = [], {"traces": 0, "refs": 0, "summary_trials": 0, "summary_with_trace": 0, "unlisted_traces": 0}, []
    run_dirs = []
    for r in args.runs:
        run_dir = pathlib.Path(r)
        if not run_dir.is_dir():
            problems.append(f"{r}: not a directory")
            continue
        run_dirs.append(run_dir)
        check_run(run_dir, root, problems, stats)
    if args.harbor:
        harbor_sample(run_dirs, args.harbor, problems, notes)
    for p in problems:
        print(p)
    for n in notes:
        print(f"note: {n}")
    print(f"checked {stats['traces']} trace(s), {stats['refs']} blob reference(s), "
          f"{stats['summary_with_trace']} summary trial(s) with a trace, "
          f"{stats['unlisted_traces']} trace(s) superseded by retries; {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
