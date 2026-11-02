#!/usr/bin/env python3
"""Run every tbench verify.sh on the host: gold must pass everything, the untouched
workspace must fail (except tasks where doing nothing is the right answer)."""
import json, pathlib, shutil, subprocess, sys, tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2] / "suites/tbench"
DO_NOTHING_OK = {"missing-data"}
bad = 0


def run(tid, apply_gold):
    with tempfile.TemporaryDirectory() as tmp:
        proj = pathlib.Path(tmp, "project")
        shutil.copytree(ROOT / "workspaces" / tid, proj, symlinks=True)
        if apply_gold:
            r = subprocess.run(["cp", "-a", str(ROOT / "verify" / tid / "gold") + "/.", str(proj)], capture_output=True)
            if r.returncode:
                return None, f"gold tree does not copy: {r.stderr.decode()[:200]}"
        script = (ROOT / "verify" / tid / "verify.sh").read_text().replace("/home/dev/project", str(proj))
        r = subprocess.run(["bash", "-c", script], capture_output=True, text=True, cwd=tmp, timeout=120)
        res = {}
        for l in r.stdout.splitlines():
            if l.startswith(("PASS ", "FAIL ")):
                res[l[5:]] = l[:4]
        return res, ""


for line in open(ROOT / "prompts.jsonl"):
    t = json.loads(line)
    tid = t["id"]
    want = json.load(open(ROOT / "verify" / tid / "tests.json"))["fail_to_pass"]
    gold, err = run(tid, True)
    if gold is None:
        print(f"{tid:22} ERROR {err}"); bad += 1; continue
    missing = [n for n in want if n not in gold]
    failed = [n for n, v in gold.items() if v != "PASS"]
    base, _ = run(tid, False)
    base_pass = [n for n, v in base.items() if v == "PASS"]
    if tid in DO_NOTHING_OK:
        ok = not missing and not failed and len(base_pass) == len(want)
    else:
        ok = not missing and not failed and len(base_pass) < len(want)
    print(f"{tid:22} {'ok ' if ok else 'BAD'} gold {len(gold)-len(failed)}/{len(want)} pass; untouched workspace passes {len(base_pass)}/{len(want)} {('missing:'+str(missing)) if missing else ''} {('gold-fail:'+str(failed)) if failed else ''}")
    bad += not ok
sys.exit(1 if bad else 0)
