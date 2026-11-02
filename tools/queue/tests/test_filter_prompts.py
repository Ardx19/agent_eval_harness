"""Tests for tools/queue/filter_prompts.py."""
import json
import pathlib
import subprocess
import sys

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "filter_prompts.py"
IDS = ["p33", "p34", "p35", "p38"]


def make_src(d: pathlib.Path) -> pathlib.Path:
    src = d / "prompts.jsonl"
    src.write_text("".join(json.dumps({"id": i, "prompt": "x"}) + "\n" for i in IDS))
    return src


def run(src, dst, *extra):
    p = subprocess.run([sys.executable, str(SCRIPT), str(src), str(dst), *extra], capture_output=True, text=True)
    return p.returncode, p.stdout.strip()


def ids(path):
    return [json.loads(l)["id"] for l in path.read_text().splitlines()]


def test_skip_drops_ids_and_keeps_order(tmp_path):
    src = make_src(tmp_path)
    dst = tmp_path / "out.jsonl"
    code, n = run(src, dst, "--skip", "p34")
    assert code == 0 and n == "3"
    assert ids(dst) == ["p33", "p35", "p38"]


def test_only_keeps_listed_ids(tmp_path):
    src = make_src(tmp_path)
    dst = tmp_path / "out.jsonl"
    run(src, dst, "--only", "p35,p38")
    assert ids(dst) == ["p35", "p38"]


def test_two_only_lists_intersect(tmp_path):
    src = make_src(tmp_path)
    dst = tmp_path / "out.jsonl"
    run(src, dst, "--only", "p33,p35", "--only", "p35,p38")
    assert ids(dst) == ["p35"]


def test_source_is_unchanged(tmp_path):
    src = make_src(tmp_path)
    before = src.read_bytes()
    run(src, tmp_path / "out.jsonl", "--skip", "p33,p34")
    assert src.read_bytes() == before


def test_no_filters_copies_everything(tmp_path):
    src = make_src(tmp_path)
    dst = tmp_path / "out.jsonl"
    run(src, dst)
    assert ids(dst) == IDS
