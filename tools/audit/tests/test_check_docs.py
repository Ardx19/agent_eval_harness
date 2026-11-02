"""Fixture tests for tools/audit/check_docs.py against a fake repository and a fake
harness binary with a flag-package style usage block."""
import pathlib
import stat
import subprocess
import sys

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "check_docs.py"

FAKE_HARNESS = '''#!/usr/bin/env python3
import sys
USAGE = {
  "run": "Usage of run:\\n  -profile string\\n    \\tprofile\\n  -prompts string\\n    \\tprompts\\n",
  "grade": "Usage of grade:\\n  -run string\\n    \\trun dir\\n",
  "doctor": "Usage of doctor:\\n",
}
args = sys.argv[1:]
if not args:
    print("harness: subcommands\\n  run       run trials\\n  grade     grade runs\\n  doctor    check the machine")
    sys.exit(0)
cmd = args[0]
if cmd in USAGE and args[-1] == "-h":
    sys.stderr.write(USAGE[cmd]); sys.exit(0)
print("unknown", cmd); sys.exit(2)
'''


def make_repo(tmp: pathlib.Path, doc: str) -> pathlib.Path:
    (tmp / "bin").mkdir()
    h = tmp / "bin" / "harness"
    h.write_text(FAKE_HARNESS)
    h.chmod(h.stat().st_mode | stat.S_IEXEC)
    (tmp / "src").mkdir()
    (tmp / "src" / "main.go").write_text("package main\nfunc main() {}\n")
    (tmp / "docs").mkdir()
    (tmp / "README.md").write_text(doc)
    return tmp


def check(tmp: pathlib.Path):
    p = subprocess.run([sys.executable, str(SCRIPT), "--root", str(tmp)], capture_output=True, text=True)
    return p.returncode, p.stdout


def test_clean_document_passes(tmp_path):
    root = make_repo(tmp_path, "Run `bin/harness run -profile p -prompts x` after `bin/harness doctor`.\n"
                               "See `src/main.go:2` and the grade command `harness grade -run runs/x`.\n")
    code, out = check(root)
    assert code == 0, out
    assert "0 problem(s)" in out


def test_unknown_flag_in_code_span_is_reported(tmp_path):
    root = make_repo(tmp_path, "Use `bin/harness run -profile p -bogus 3`.\n")
    code, out = check(root)
    assert code == 1
    assert "has no flag -bogus" in out


def test_unknown_command_in_code_span_is_reported(tmp_path):
    root = make_repo(tmp_path, "Call `bin/harness frobnicate` to start.\n")
    code, out = check(root)
    assert code == 1
    assert "`harness frobnicate` is not a command" in out


def test_prose_word_after_harness_is_not_a_command(tmp_path):
    root = make_repo(tmp_path, "The harness works well and the harness does its job.\n")
    code, out = check(root)
    assert code == 0, out


def test_bad_flag_in_fenced_block_is_reported(tmp_path):
    root = make_repo(tmp_path, "```bash\nbin/harness grade -run runs/x -nonsense\n```\n")
    code, out = check(root)
    assert code == 1
    assert "has no flag -nonsense" in out


def test_missing_repo_path_is_reported(tmp_path):
    root = make_repo(tmp_path, "The logic lives in `internal/ghost/ghost.go`.\n")
    code, out = check(root)
    assert code == 1
    assert "path `internal/ghost/ghost.go` does not exist" in out


def test_file_line_past_the_end_is_reported(tmp_path):
    root = make_repo(tmp_path, "The entry point is `src/main.go:99`.\n")
    code, out = check(root)
    assert code == 1
    assert "past the end of the file" in out


def test_placeholders_and_generated_outputs_are_skipped(tmp_path):
    root = make_repo(tmp_path, "Write `profiles/*.yaml` and `runs/<id>/report.md` and `trace.json`.\n")
    code, out = check(root)
    assert code == 0, out
