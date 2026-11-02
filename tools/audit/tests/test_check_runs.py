"""Fixture tests for tools/audit/check_runs.py: a clean run passes, and each kind of
broken evidence is reported."""
import hashlib
import json
import pathlib
import subprocess
import sys

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "check_runs.py"


def make_run(root: pathlib.Path, blob_bytes=b"payload", schema="evalharness-2", listed=True):
    """Build root/runs/r1 with one trial whose trace references one blob."""
    run = root / "runs" / "r1"
    trial = run / "t01" / "trial-1"
    (trial / "blobs").mkdir(parents=True)
    digest = hashlib.sha256(blob_bytes).hexdigest()
    (trial / "blobs" / digest).write_bytes(blob_bytes)
    trace = {"schema_version": schema, "task_id": "t01", "host": {"call_records": f"sha256:{digest}"}}
    (trial / "trace.json").write_text(json.dumps(trace))
    trace_path = f"runs/r1/t01/trial-1/trace.json" if listed else "runs/r1/t01/trial-9/trace.json"
    (run / "summary.json").write_text(json.dumps({"trials": [{"task_id": "t01", "trial": 1, "trace_path": trace_path}]}))
    return run, trial, digest


def check(root: pathlib.Path, run: pathlib.Path):
    p = subprocess.run([sys.executable, str(SCRIPT), "--root", str(root), str(run)], capture_output=True, text=True)
    return p.returncode, p.stdout


def test_clean_run_passes(tmp_path):
    run, _, _ = make_run(tmp_path)
    code, out = check(tmp_path, run)
    assert code == 0, out
    assert "0 problem(s)" in out


def test_deleted_blob_is_dangling(tmp_path):
    run, trial, digest = make_run(tmp_path)
    (trial / "blobs" / digest).unlink()
    code, out = check(tmp_path, run)
    assert code == 1
    assert f"dangling reference sha256:{digest}" in out


def test_blob_hash_mismatch_is_reported(tmp_path):
    run, trial, digest = make_run(tmp_path)
    (trial / "blobs" / digest).write_bytes(b"tampered")
    code, out = check(tmp_path, run)
    assert code == 1
    assert "hashes to" in out


def test_listed_trace_that_is_missing_is_reported(tmp_path):
    run, _, _ = make_run(tmp_path, listed=False)
    code, out = check(tmp_path, run)
    assert code == 1
    assert "does not exist" in out


def test_wrong_schema_version_is_reported(tmp_path):
    run, _, _ = make_run(tmp_path, schema="evalharness-1")
    code, out = check(tmp_path, run)
    assert code == 1
    assert "schema_version" in out


def test_trace_that_does_not_decode_is_reported(tmp_path):
    run, trial, _ = make_run(tmp_path)
    (trial / "trace.json").write_text("{not json")
    code, out = check(tmp_path, run)
    assert code == 1
    assert "does not decode" in out
