#!/usr/bin/env python3
"""Build prompts.jsonl and expectations.jsonl for chosen break-test prompts.

    tools/suite_from_testinfo.py --src /path/test_info.txt --ids 39,40,43 --out suites/initial --prefix p --append

Reads the numbered prompts and the "Rule:" rubric lines from test_info.txt (never
modified). The prompt text goes to prompts.jsonl (what the agent sees); the rubric
goes to expectations.jsonl (host-only). Extra input files can be given with
--file ID=path/in/suite (the file must already exist under --out).
"""
import argparse, json, pathlib, re, sys

ap = argparse.ArgumentParser()
ap.add_argument("--src", required=True)
ap.add_argument("--ids", required=True)
ap.add_argument("--out", required=True)
ap.add_argument("--prefix", default="p")
ap.add_argument("--tags", default="")
ap.add_argument("--append", action="store_true")
ap.add_argument("--file", action="append", default=[])
ap.add_argument("--wall", action="append", default=[], help="ID=seconds override")
a = ap.parse_args()

prompts, rubrics = {}, {}
for line in pathlib.Path(a.src).read_text().splitlines():
    m = re.match(r"^(\d+)\. ([A-Z](?:: [A-Z+]+)?)(, COMPOUND)? (.*)$", line)
    if m and not m.group(4).startswith("Rule:"):
        prompts[int(m.group(1))] = (m.group(2), bool(m.group(3)), m.group(4).strip())
        continue
    r = re.match(r"^(\d+)\. (Rule: .*)$", line)
    if r:
        rubrics[int(r.group(1))] = r.group(2).strip()

out = pathlib.Path(a.out)
out.mkdir(parents=True, exist_ok=True)
files = dict(f.split("=", 1) for f in a.file)
walls = dict(w.split("=", 1) for w in a.wall)
mode = "a" if a.append else "w"
n = 0
with open(out / "prompts.jsonl", mode) as pf, open(out / "expectations.jsonl", mode) as ef:
    for s in a.ids.split(","):
        i = int(s)
        if i not in prompts:
            sys.exit(f"prompt {i} not found in {a.src}")
        cat, compound, text = prompts[i]
        tid = f"{a.prefix}{i}"
        row = {"id": tid, "prompt": text, "tags": [t for t in a.tags.split(",") if t] + [f"cat-{cat[0]}"] + (["compound"] if compound else [])}
        if s in files:
            if not (out / files[s]).exists():
                sys.exit(f"input {files[s]} does not exist under {out}")
            row["inputs"] = [files[s]]
        if s in walls:
            row["limits"] = {"wall_s": int(walls[s])}
        pf.write(json.dumps(row) + "\n")
        ef.write(json.dumps({"id": tid, "category": cat[0], "compound": compound, "rubric": rubrics.get(i, "")}) + "\n")
        n += 1
print(n, "prompts written to", out)
