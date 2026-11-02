#!/usr/bin/env python3
"""Write suites/swe-lite/prompts.jsonl for the given instance ids (envs must be built).

    tools/swe/make_suite.py pytest-dev__pytest-7432 ...
The prompt is the issue text and nothing else about the evaluation. Hidden tests,
the gold patch and test lists stay under suites/swe-lite/verify/ and are never staged.
"""
import json, pathlib, sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
TEMPLATE = """Fix the following issue in this repository (a checkout of {repo}). Make the change in the source code, keep the existing behaviour that is not part of the issue working, and finish with a short summary of what you changed.

<issue>
{issue}
</issue>"""

lines = []
for iid in sys.argv[1:]:
    b = ROOT / "envs/build" / iid
    t = json.load(open(ROOT / "suites/swe-lite/verify" / iid / "tests.json"))
    issue = (b / "problem.txt").read_text().strip()
    lines.append(json.dumps({"id": iid, "prompt": TEMPLATE.format(repo=t["repo"], issue=issue), "env": iid,
                             "verify": f"verify/{iid}", "tags": ["swe-lite", t["repo"].split("/")[1]]}))
(ROOT / "suites/swe-lite/prompts.jsonl").write_text("\n".join(lines) + "\n")
print(len(lines), "tasks")
