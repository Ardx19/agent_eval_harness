"""Tests for tools/queue/check_prompts.py: every path a prompt names must exist before a
stage starts, relative to the prompts file's directory."""
import json
import pathlib
import subprocess
import sys

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "check_prompts.py"


def write_prompts(d: pathlib.Path, lines):
    p = d / "prompts.jsonl"
    p.write_text("".join(json.dumps(x) + "\n" for x in lines))
    return p


def run(prompts: pathlib.Path):
    p = subprocess.run([sys.executable, str(SCRIPT), str(prompts)], capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def test_all_inputs_present_passes(tmp_path):
    (tmp_path / "runs" / "btK1").mkdir(parents=True)
    (tmp_path / "runs" / "btK1" / "smiles.txt").write_text("CCO\n")
    p = write_prompts(tmp_path, [{"id": "p1", "prompt": "x", "inputs": ["runs/btK1"]}])
    code, out = run(p)
    assert code == 0, out


def test_missing_input_fails_and_names_the_task(tmp_path):
    p = write_prompts(tmp_path, [{"id": "p42", "prompt": "x", "inputs": ["runs/gone/smiles.txt"]}])
    code, out = run(p)
    assert code == 1
    assert "p42" in out and "runs/gone/smiles.txt" in out


def test_missing_workspace_and_verify_are_reported(tmp_path):
    p = write_prompts(tmp_path, [
        {"id": "c1", "prompt": "x", "workspace": "ws-missing"},
        {"id": "c2", "prompt": "x", "verify": "verify-missing"},
    ])
    code, out = run(p)
    assert code == 1
    assert "c1" in out and "ws-missing" in out
    assert "c2" in out and "verify-missing" in out


def test_absolute_or_escaping_input_is_rejected(tmp_path):
    p = write_prompts(tmp_path, [
        {"id": "a1", "prompt": "x", "inputs": ["/etc/passwd"]},
        {"id": "a2", "prompt": "x", "inputs": ["../outside"]},
    ])
    code, out = run(p)
    assert code == 1
    assert "a1" in out and "a2" in out


def test_unparseable_line_is_reported_with_its_number(tmp_path):
    p = tmp_path / "prompts.jsonl"
    p.write_text('{"id": "ok", "prompt": "x"}\n{not json\n')
    code, out = run(p)
    assert code == 1
    assert ":2" in out


def test_missing_prompts_file_fails(tmp_path):
    code, out = run(tmp_path / "nope.jsonl")
    assert code == 1
