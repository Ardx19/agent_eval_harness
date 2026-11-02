#!/usr/bin/env python3
"""Write a filtered copy of a prompts file: drop ids in --skip, keep only ids in every
non-empty --only list. The source file is never modified.

Usage:
  python3 tools/queue/filter_prompts.py SRC DST [--skip ID,ID] [--only ID,ID] [--only ID,ID]

Prints the number of tasks written.
"""
import argparse
import json
import sys


def ids_of(text):
    return {x for x in (text or "").split(",") if x}


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("--skip", default="")
    ap.add_argument("--only", action="append", default=[])
    args = ap.parse_args(argv)
    skip = ids_of(args.skip)
    only_sets = [s for s in (ids_of(o) for o in args.only) if s]
    kept = []
    with open(args.src) as f:
        for raw in f:
            if not raw.strip():
                continue
            tid = json.loads(raw)["id"]
            if tid in skip or any(tid not in s for s in only_sets):
                continue
            kept.append(raw if raw.endswith("\n") else raw + "\n")
    with open(args.dst, "w") as f:
        f.writelines(kept)
    print(len(kept))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
