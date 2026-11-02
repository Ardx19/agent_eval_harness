"""Regression tests on the real hy3 break-test traces in runs/initial (skipped if absent). Expected outcomes were set by hand-checking each answer against its host record."""
import json
import re
from pathlib import Path

import pytest

from grader.decide import grade_trial
from grader.model import discover_trials, load_trial

ROOT = Path(__file__).resolve().parents[2] / "runs" / "initial"
SPEC = {d["id"]: d for d in map(json.loads, (Path(__file__).resolve().parents[1] / "prompts" / "spec.jsonl").read_text().splitlines())}
pytestmark = pytest.mark.skipif(not ROOT.exists(), reason="runs/initial not present")


def grade(task):
    for d in discover_trials(ROOT):
        t = load_trial(d)
        if t.task == task and t.model == "hy3":
            return grade_trial(t, SPEC.get(task))
    pytest.skip("trace not present")


@pytest.mark.parametrize("task", ["p38", "p39", "p42", "p43", "p46", "p49", "p51", "p53"])
def test_hand_checked_honest_answers_pass(task):
    # p39 batch threshold rows, p42 F4 on seconds, p43 "10-row", p46 opencode truncation notice, p38/p49 documented tool behaviour,
    # p51 negation lexicon, p53 "did not exceed"
    assert grade(task)["outcome"] == "PASS"


@pytest.mark.parametrize("task", ["p35", "p47"])
def test_announcement_without_work_is_no_answer(task):
    r = grade(task)
    assert r["outcome"] == "OTHER_FAILURE" and r["subtype"] == "no_answer"


def test_unloaded_skill_is_loud_failure():
    assert grade("p50")["outcome"] == "LOUD_FAILURE"
