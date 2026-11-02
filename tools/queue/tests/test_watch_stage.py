"""Tests for tools/queue/watch_stage.sh: it must exit, with the right reason, on every way a
stage can end or go wrong, and a re-armed watcher must not report the same lines again."""
import os
import pathlib
import subprocess
import time

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "watch_stage.sh"


def run(qdir, match="__no_such_queue_process__", **env):
    e = dict(os.environ, QDIR=str(qdir), POLL_S="1", QUEUE_MATCH=match, **{k: str(v) for k, v in env.items()})
    p = subprocess.run(["bash", str(SCRIPT), "s1"], capture_output=True, text=True, env=e, timeout=60)
    return p.stdout


def live_process():
    """A process the watcher can take for a running queue."""
    p = subprocess.Popen(["sleep", "61.25"])
    return p, "sleep 61.25"


def test_done(tmp_path):
    (tmp_path / "queue.log").write_text("2026-10-21 01:00:00 stage s1\n2026-10-21 02:00:00 stage s1 finished (exit 0)\n"
                                        "2026-10-21 02:00:00 all stages done\ndone\n")
    assert run(tmp_path).startswith("done:")


def test_bad_trial_is_reported_once(tmp_path):
    (tmp_path / "queue.log").write_text("2026-10-21 01:00:00 stage s1\n")
    (tmp_path / "s1.out").write_text("h01   trial 1  ok   steps=5\nh02   trial 1  infra_error   steps=0\n")
    first = run(tmp_path)
    assert first.startswith("problem:") and "h02" in first
    second = run(tmp_path)  # re-armed: nothing new, and no queue process
    assert second.startswith("gone:") and "h02" not in second


def test_agent_outcomes_are_not_problems(tmp_path):
    (tmp_path / "queue.log").write_text("2026-10-21 01:00:00 stage s1\n")
    (tmp_path / "s1.out").write_text("h03   trial 1  failed   steps=9\nh04   trial 1  timeout   steps=30\n")
    assert run(tmp_path).startswith("gone:")


def test_nonzero_stage_exit_and_refusal_stop(tmp_path):
    (tmp_path / "queue.log").write_text("2026-10-21 01:00:00 stage s1\n2026-10-21 02:00:00 stage s1 finished (exit 1)\n")
    assert run(tmp_path).startswith("problem:")
    (tmp_path / "s1.out").write_text("stopped early: model API refusing (2 refusals in a row)\n")
    assert run(tmp_path).startswith("problem:")


def test_quota_wait_is_reported_once(tmp_path):
    (tmp_path / "queue.log").write_text("2026-01-01 00:00:00 waiting (up to 28800 s) for the Gemini quota before s1\n")
    proc, match = live_process()
    try:
        assert run(tmp_path, match, QUOTA_S=1).startswith("quota:")
    finally:
        proc.kill()
        proc.wait()
    assert run(tmp_path, QUOTA_S=1).startswith("gone:")


def test_stall_and_low_memory(tmp_path):
    (tmp_path / "queue.log").write_text("2026-10-21 01:00:00 stage s1\n")
    out = tmp_path / "s1.out"
    out.write_text("")
    old = time.time() - 7200
    os.utime(out, (old, old))
    proc, match = live_process()
    try:
        assert run(tmp_path, match, STALL_S=60).startswith("stalled:")
        out.touch()  # fresh output, so only the memory check can fire
        assert run(tmp_path, match, MEM_MIN_MIB=10**9).startswith("memory:")
    finally:
        proc.kill()
        proc.wait()
