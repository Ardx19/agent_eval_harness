"""Behaviour tests for tools/queue/run_queue.sh. Each test builds a small fake repository
(the real queue script and helpers, a fake bin/harness, a fake gemini_gate.sh) and runs the
script in it, so the stage logic, the caps and the file handling are exercised for real."""
import json
import os
import pathlib
import shutil
import subprocess
import time

ROOT = pathlib.Path(__file__).resolve().parents[3]
QUEUE = ROOT / "tools" / "queue"

FAKE_HARNESS = """#!/bin/bash
echo "$@" >> "$FAKE_LOG"
if [ "$1" = run ] && [ -n "$HANG" ]; then
  sleep 987654 &
  sleep 987654
fi
exit 0
"""

FAKE_GATE = """#!/bin/bash
echo "probe $1" >> "$FAKE_LOG.gate"
[ -z "$GATE_FAIL" ]
"""

CLEAR = ("ONLY", "DRY", "SKIP", "TASKS", "STAGE_CAP_S", "KILL_GRACE_S", "GATE_WAIT_S", "HANG", "GATE_FAIL", "FAKE_LOG")


def make_repo(tmp: pathlib.Path) -> pathlib.Path:
    """A repository with the queue scripts, a fake harness and the prompt files the default
    stages name (the tests override them where they need a particular shape)."""
    (tmp / "tools" / "queue").mkdir(parents=True)
    for name in ("run_queue.sh", "check_prompts.py", "filter_prompts.py"):
        shutil.copy(QUEUE / name, tmp / "tools" / "queue" / name)
    (tmp / "tools" / "queue" / "run_queue.sh").chmod(0o755)
    gate = tmp / "tools" / "queue" / "gemini_gate.sh"
    gate.write_text(FAKE_GATE)
    gate.chmod(0o755)
    (tmp / "bin").mkdir()
    h = tmp / "bin" / "harness"
    h.write_text(FAKE_HARNESS)
    h.chmod(0o755)
    (tmp / "profiles").mkdir()
    (tmp / "suites" / "initial").mkdir(parents=True)
    (tmp / "suites" / "initial" / "runs" / "btK1").mkdir(parents=True)
    (tmp / "suites" / "initial" / "runs" / "btK1" / "smiles.txt").write_text("CCO\n")
    rows = [{"id": f"p{n}", "prompt": "x"} for n in (33, 34, 35, 38)]
    (tmp / "suites" / "initial" / "prompts.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows))
    (tmp / "suites" / "detector-eval").mkdir(parents=True)
    for name in ("prompts.jsonl", "prompts.gemini-rest.jsonl"):
        (tmp / "suites" / "detector-eval" / name).write_text(json.dumps({"id": "t01", "prompt": "x"}) + "\n")
    (tmp / "suites" / "detector-eval" / "pack").mkdir()
    (tmp / "runs").mkdir()
    return tmp


def run_queue(repo: pathlib.Path, timeout=60, **env):
    e = {k: v for k, v in os.environ.items() if k not in CLEAR}
    e["FAKE_LOG"] = str(repo / "harness-calls.log")
    e.update({k: str(v) for k, v in env.items()})
    p = subprocess.run(["bash", str(repo / "tools" / "queue" / "run_queue.sh")], cwd=repo, env=e,
                       capture_output=True, text=True, timeout=timeout)
    return p.returncode, p.stdout + p.stderr


def harness_calls(repo: pathlib.Path):
    f = repo / "harness-calls.log"
    return f.read_text().splitlines() if f.exists() else []


def gate_calls(repo: pathlib.Path):
    f = repo / "harness-calls.log.gate"
    return f.read_text().splitlines() if f.exists() else []


def queue_log(repo: pathlib.Path):
    f = repo / "runs" / "queue" / "queue.log"
    return f.read_text() if f.exists() else ""


def test_unknown_stage_in_only_stops_before_anything_runs(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, ONLY="gemini-typo")
    assert code == 2
    assert "unknown stage in ONLY: gemini-typo" in out
    assert harness_calls(repo) == []


def test_dry_run_lists_default_stages_and_runs_nothing(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, DRY=1)
    assert code == 0
    assert "would run: initial-hy3" in out
    assert "would run: gemini-rest" in out and "waits for Gemini quota" in out
    # opt-in stages are not in the default run
    assert "would run: gemini-break" not in out
    assert "would run: initial-deepseek" not in out
    assert harness_calls(repo) == [] and gate_calls(repo) == []


def test_missing_input_stops_the_stage_before_any_trial(tmp_path):
    repo = make_repo(tmp_path)
    (repo / "suites" / "initial" / "prompts.jsonl").write_text(
        json.dumps({"id": "p33", "prompt": "x", "inputs": ["runs/gone/smiles.txt"]}) + "\n")
    code, out = run_queue(repo, ONLY="initial-hy3")
    assert code == 3
    assert "runs/gone/smiles.txt" in out
    assert harness_calls(repo) == []
    assert "stopped before any trial" in queue_log(repo)


def test_skip_writes_a_filtered_copy_outside_suites_and_keeps_inputs_resolving(tmp_path):
    repo = make_repo(tmp_path)
    src = repo / "suites" / "initial" / "prompts.jsonl"
    (repo / "suites" / "initial" / "prompts.jsonl").write_text(
        json.dumps({"id": "p33", "prompt": "x", "inputs": ["runs/btK1"]}) + "\n" +
        json.dumps({"id": "p34", "prompt": "x"}) + "\n")
    before = src.read_bytes()
    code, out = run_queue(repo, ONLY="initial-hy3", SKIP="p34")
    assert code == 0, out
    filtered = repo / "runs" / "queue" / "filtered" / "initial-hy3" / "prompts.jsonl"
    assert filtered.is_file()
    assert [json.loads(l)["id"] for l in filtered.read_text().splitlines()] == ["p33"]
    assert src.read_bytes() == before, "the source prompts must not change"
    assert not list((repo / "suites" / "initial").glob(".filtered*")), "no hidden copies beside the source"
    call = harness_calls(repo)[0]
    assert "runs/queue/filtered/initial-hy3/prompts.jsonl" in call


def test_gated_stage_does_not_run_when_the_quota_does_not_come_back(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, ONLY="gemini-break", GATE_FAIL=1)
    assert code == 0
    assert gate_calls(repo), "the gate must be consulted"
    assert harness_calls(repo) == []
    assert "Gemini quota did not come back; stage gemini-break skipped" in queue_log(repo)


def test_gated_stage_runs_when_the_gate_passes(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, ONLY="gemini-break")
    assert code == 0
    assert any("run --profile profiles/retro-eval.yaml" in c for c in harness_calls(repo))


def test_missing_held_out_prompts_fail_before_any_gemini_probe(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, ONLY="gemini-heldout")
    assert code == 3
    assert gate_calls(repo) == [], "no quota probe may be spent on a stage that cannot start"
    assert harness_calls(repo) == []


def test_smoke_stage_runs_only_its_two_prompts(tmp_path):
    repo = make_repo(tmp_path)
    code, out = run_queue(repo, ONLY="gemini-smoke")
    assert code == 0, out
    filtered = repo / "runs" / "queue" / "filtered" / "gemini-smoke" / "prompts.jsonl"
    assert [json.loads(l)["id"] for l in filtered.read_text().splitlines()] == ["p35", "p38"]


def test_stage_cap_stops_the_whole_process_group(tmp_path):
    repo = make_repo(tmp_path)
    start = time.time()
    code, out = run_queue(repo, timeout=90, ONLY="initial-hy3", HANG=1, STAGE_CAP_S=2, KILL_GRACE_S=1)
    elapsed = time.time() - start
    assert code == 0, out
    assert elapsed < 30, f"the cap must end the stage, took {elapsed:.1f}s"
    assert "hit its 2 s cap" in queue_log(repo)
    time.sleep(0.5)
    left = subprocess.run(["pgrep", "-f", "sleep 987654"], capture_output=True, text=True).stdout.split()
    for pid in left:  # never leave the fake children running, even when the check fails
        os.kill(int(pid), 9)
    assert left == [], f"a child process outlived the cap: pids {left}"


def test_report_never_deletes_files_in_a_pack(tmp_path):
    repo = make_repo(tmp_path)
    labels = repo / "suites" / "detector-eval" / "pack" / "labels.jsonl"
    labels.write_text('{"id":"e-1","label":"honest","split":"dev"}\n')
    code, out = run_queue(repo, ONLY="report")
    assert code == 0, out
    assert labels.read_text().startswith('{"id":"e-1"'), "labels in a pack must survive the report stage"
    assert any("review-pack --out runs/queue/packs/pack-" in c for c in harness_calls(repo))
