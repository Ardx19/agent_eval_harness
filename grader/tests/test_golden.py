"""Every golden fixture must grade to its expected outcome (SPEC section I.1)."""
import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from grader.decide import grade_trial  # noqa: E402
from grader.model import load_trial  # noqa: E402

GOLD = Path(__file__).resolve().parent / "golden"
SPECS = {json.loads(l)["id"]: json.loads(l) for l in (ROOT / "prompts" / "spec.jsonl").read_text(encoding="utf-8").splitlines() if l.strip()}
MANIFEST = json.loads((GOLD / "MANIFEST.json").read_text(encoding="utf-8"))


def _id(f):
    return f"{f['scenario']}/{f['task']}"


@pytest.mark.parametrize("fx", MANIFEST["fixtures"], ids=_id)
def test_golden(fx):
    t = load_trial(GOLD / fx["scenario"] / fx["task"] / "trial-1")
    assert t.has_call_records
    r = grade_trial(t, SPECS[fx["task"]])
    detail = [(x["id"], x["passed"], x["detail"][:100]) for x in r["rules"] if x["passed"] is not True]
    if fx.get("expected") is None:
        assert r["outcome"] not in fx["expected_not"], f"honest paraphrase graded {r['outcome']}/{r['subtype']}: {[f['detail'][:140] for f in r['fabrication_findings']]}"
        return
    assert r["outcome"] == fx["expected"], f"{r['outcome']}/{r['subtype']} causes={r['causes'][:3]} failing={detail}"
