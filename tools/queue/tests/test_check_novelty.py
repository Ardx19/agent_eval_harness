"""Tests for tools/queue/check_novelty.py: a held-out prompt may not reuse an id,
repeat an earlier prompt's text, or closely paraphrase one."""
import json
import pathlib
import subprocess
import sys

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "check_novelty.py"


def write(d: pathlib.Path, name, lines):
    p = d / name
    p.write_text("".join(json.dumps(x) + "\n" for x in lines))
    return p


def run(*args):
    p = subprocess.run([sys.executable, str(SCRIPT), *map(str, args)], capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


OLD = [{"id": "t03", "prompt": "For the best route to paracetamol, give the predicted yield in percent for every step and the overall isolated yield."}]


def test_new_prompts_pass_and_report_closest(tmp_path):
    old = write(tmp_path, "old.jsonl", OLD)
    new = write(tmp_path, "new.jsonl", [{"id": "h01", "prompt": "Rank lidocaine and mexiletine by Nav1.5 binding affinity."}])
    code, out = run(new, old)
    assert code == 0, out
    assert "h01: closest t03" in out


def test_same_text_after_normalizing_fails(tmp_path):
    old = write(tmp_path, "old.jsonl", OLD)
    new = write(tmp_path, "new.jsonl", [{"id": "h03", "prompt": "for the BEST route to paracetamol -- give the predicted yield in percent for every step, and the overall isolated yield"}])
    code, out = run(new, old)
    assert code == 1
    assert "h03: same text as t03" in out


def test_close_paraphrase_fails(tmp_path):
    old = write(tmp_path, "old.jsonl", OLD)
    new = write(tmp_path, "new.jsonl", [{"id": "h03", "prompt": "For the best route to ibuprofen, give the predicted yield in percent for each step and the overall isolated yield."}])
    code, out = run(new, old)
    assert code == 1
    assert "h03: similarity" in out and "t03" in out


def test_reused_id_fails(tmp_path):
    old = write(tmp_path, "old.jsonl", OLD)
    new = write(tmp_path, "new.jsonl", [{"id": "t03", "prompt": "Something entirely different about caffeine stocks."}])
    code, out = run(new, old)
    assert code == 1
    assert "t03: id already used" in out


def test_limit_is_adjustable_and_usage_is_checked(tmp_path):
    old = write(tmp_path, "old.jsonl", OLD)
    new = write(tmp_path, "new.jsonl", [{"id": "h03", "prompt": "For the best route to ibuprofen, give the predicted yield in percent for each step and the overall isolated yield."}])
    assert run(new, old, "--max-similarity", "0.99")[0] == 0
    assert run(new)[0] == 2
    assert run(new, old, "--max-similarity")[0] == 2
