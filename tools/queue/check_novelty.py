#!/usr/bin/env python3
"""Check that a new prompts file repeats nothing from earlier prompt sets.

A held-out set is only held out if none of its prompts were seen before. This
checker compares each new prompt with every prompt in the earlier files:

- the same task id is an error;
- the same text after normalizing (case, punctuation, whitespace) is an error;
- a similarity at or above --max-similarity (difflib ratio on the normalized
  text, default 0.8) is an error, as a close paraphrase.

It also prints, for each new prompt, its closest earlier prompt and the
similarity, so a person can judge the borderline ones.

Usage:
  python3 tools/queue/check_novelty.py NEW.jsonl EARLIER.jsonl [EARLIER.jsonl ...] [--max-similarity 0.8]

Exit 0 when nothing repeats, 1 otherwise, 2 on bad usage.
"""
import difflib
import json
import pathlib
import re
import sys


def normalize(text: str) -> str:
    """Lower case, punctuation removed, whitespace collapsed."""
    return " ".join(re.sub(r"[^\w\s]", " ", text.lower()).split())


def load(path: pathlib.Path):
    """(id, prompt) pairs from a prompts file; blank and # lines are skipped."""
    out = []
    for raw in path.read_text().splitlines():
        if raw.strip() and not raw.lstrip().startswith("#"):
            task = json.loads(raw)
            out.append((task.get("id", ""), task.get("prompt", "")))
    return out


def check(new: pathlib.Path, earlier: list, max_similarity: float):
    """Problems (errors) and a report line per new prompt."""
    seen = []
    for p in earlier:
        seen += [(p, tid, text, normalize(text)) for tid, text in load(p)]
    problems, report = [], []
    for tid, text in load(new):
        norm = normalize(text)
        best = (0.0, None, None)
        for path, old_id, _, old_norm in seen:
            if tid == old_id:
                problems.append(f"{tid}: id already used in {path}")
            if norm == old_norm:
                problems.append(f"{tid}: same text as {old_id} in {path}")
                continue
            r = difflib.SequenceMatcher(None, norm, old_norm, autojunk=False).ratio()
            if r > best[0]:
                best = (r, old_id, path)
        r, old_id, path = best
        if r >= max_similarity:
            problems.append(f"{tid}: similarity {r:.2f} to {old_id} in {path} (limit {max_similarity})")
        report.append(f"{tid}: closest {old_id} ({path if path else '-'}) similarity {r:.2f}")
    return problems, report


def main(argv):
    max_similarity = 0.8
    if "--max-similarity" in argv:
        i = argv.index("--max-similarity")
        try:
            max_similarity = float(argv[i + 1])
        except (IndexError, ValueError):
            print(__doc__, file=sys.stderr)
            return 2
        argv = argv[:i] + argv[i + 2:]
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    problems, report = check(pathlib.Path(argv[0]), [pathlib.Path(a) for a in argv[1:]], max_similarity)
    for line in report:
        print(line)
    for p in problems:
        print("PROBLEM", p)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
