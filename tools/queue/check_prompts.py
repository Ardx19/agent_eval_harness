#!/usr/bin/env python3
"""Check that every path a prompts file names exists, before a queue stage starts.

The runner resolves a task's `inputs`, `workspace` and `verify` relative to the directory
of the prompts file, and refuses absolute or escaping paths. This checker applies the same
rules up front, so a stage fails in a second with the task and path, instead of partway
through a long run.

Usage:
  python3 tools/queue/check_prompts.py PROMPTS.jsonl

Exit 0 when every path resolves, 1 otherwise. Problems print one per line.
"""
import json
import pathlib
import sys

PATH_FIELDS = ("inputs", "workspace", "verify")


def bad_relative(p: str) -> str | None:
    """Why a path is not allowed (absolute or escaping the prompts directory), or None."""
    if p.startswith("/"):
        return "is absolute"
    parts = pathlib.PurePosixPath(p).parts
    if ".." in parts:
        return "escapes the prompts directory"
    return None


def check(prompts: pathlib.Path):
    problems = []
    if not prompts.is_file():
        return [f"{prompts}: prompts file not found"]
    base = prompts.parent
    seen = set()
    for n, raw in enumerate(prompts.read_text().splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        try:
            task = json.loads(raw)
        except json.JSONDecodeError as e:
            problems.append(f"{prompts}:{n}: not valid JSON ({e.msg})")
            continue
        tid = task.get("id", f"line {n}")
        if tid in seen:
            problems.append(f"{prompts}:{n}: duplicate task id {tid}")
        seen.add(tid)
        for field in PATH_FIELDS:
            value = task.get(field)
            if value is None:
                continue
            paths = value if isinstance(value, list) else [value]
            for p in paths:
                if not isinstance(p, str) or not p:
                    problems.append(f"{prompts}:{n}: task {tid}: {field} must be a path string")
                    continue
                why = bad_relative(p)
                if why:
                    problems.append(f"{prompts}:{n}: task {tid}: {field} {p!r} {why}")
                    continue
                if not (base / p).exists():
                    problems.append(f"{prompts}:{n}: task {tid}: {field} {p!r} not found (looked in {base / p})")
    return problems


def main(argv):
    if len(argv) != 1:
        print(__doc__, file=sys.stderr)
        return 2
    problems = check(pathlib.Path(argv[0]))
    for p in problems:
        print(p)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
