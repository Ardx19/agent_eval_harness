#!/usr/bin/env python3
"""Check that the documentation matches the code.

For each document:
  commands  `harness <cmd> ... -flag` must name a real command, and every flag on that
            line must appear in `harness <cmd> -h`. In prose, the mention must sit in a
            code span or a fenced code block; ordinary sentences are not commands.
  paths     a backticked repo path must exist. A token with a directory part is checked
            at that path. A bare file name is searched for in the repo. Generated outputs
            of a run (trace.json, report.md, ...), placeholders, absolute paths, and
            model ids with a non-repo first segment are skipped.
  lines     `file.ext:N` must name a real file with at least N lines. Files found by bare
            name are searched repo-wide.

Usage:
  python3 tools/audit/check_docs.py [--root DIR] [--harness BIN] [DOC ...]

Default documents: README.md, ARCHITECTURE.md, HANDOFF.md and docs/*.md (not docs/plans/).
Exit 0 when nothing is wrong, 1 otherwise, 2 if the harness binary is missing.
Problems print as `doc:line: message`.
"""
import argparse
import glob
import pathlib
import re
import subprocess
import sys

KNOWN_EXT = (".go", ".py", ".md", ".sh", ".yaml", ".yml", ".json", ".jsonl", ".ts", ".toml", ".csv")
# names that a run or a tool writes at runtime: they exist under runs/, not in the repo
GENERATED = {"trace.json", "trace.reanalyzed.json", "report.md", "grade.json", "console.log",
             "rubric_results.jsonl", "review_queue.jsonl", "key.jsonl", "labels.jsonl",
             "summary.json", "grade_trials.csv", "grade_rules.csv", "grade_calls.csv",
             "grade_summary.csv", "exchanges.jsonl"}
EXCLUDE_DIRS = {"envs", "runs", "images", "bin", "backups", ".git", "tools/harbor-venv", "spike"}

CMD_RX = re.compile(r"\bharness\s+([a-z][a-z-]*)\b([^`\n]*)")
FLAG_RX = re.compile(r"(?<![\w-])--?([A-Za-z][\w-]*)")
HELP_FLAG_RX = re.compile(r"^\s+-([A-Za-z][\w-]*)", re.M)
CODE_RX = re.compile(r"`([^`\n]+)`")
LINE_RX = re.compile(r"((?:[\w.-]+/)*[\w.-]+\.(?:go|py|md|sh|yaml|yml|json|jsonl|ts|toml|csv)):(\d+)")
SKIP_TOKEN = re.compile(r"[<>*{}]|\.\.\.|^https?://|^sha256:")


def help_flags(harness, cmd, cache):
    """Flags a command accepts, read from its -h output (cached). None if it has no usage."""
    if cmd not in cache:
        out = subprocess.run([harness, cmd, "-h"], capture_output=True, text=True)
        text = out.stdout + out.stderr
        cache[cmd] = set(HELP_FLAG_RX.findall(text)) if "Usage" in text else None
    return cache[cmd]


def known_commands(harness):
    """Command names from `harness` usage output."""
    out = subprocess.run([harness], capture_output=True, text=True)
    return set(re.findall(r"^\s+([a-z][a-z-]*)\s", out.stdout + out.stderr, re.M))


class Repo:
    """File lookups inside the repository, skipping generated and excluded trees."""

    def __init__(self, root):
        self.root = root
        self.top = {p.name for p in root.iterdir()}
        self._names = None

    def _index(self):
        if self._names is None:
            self._names = {}
            for p in self.root.rglob("*"):
                if not p.is_file():
                    continue
                rel = p.relative_to(self.root).as_posix()
                if rel.split("/")[0] in EXCLUDE_DIRS or any(rel.startswith(e + "/") for e in EXCLUDE_DIRS):
                    continue
                self._names.setdefault(p.name, []).append(rel)
        return self._names

    def by_name(self, name):
        return self._index().get(name, [])

    def has_path(self, rel):
        return (self.root / rel).exists()

    def repo_path(self, tok):
        """True when a slash token is a repo path (its first segment is top-level)."""
        return tok.split("/")[0] in self.top


def check_path_token(tok, repo):
    """Return a problem message for a backticked token, or None."""
    if SKIP_TOKEN.search(tok) or " " in tok:
        return None  # placeholder, URL, or a command with arguments (checked as a command)
    base = tok.split(":")[0].rstrip("/")
    if not base or base.startswith(("runs/", "/", "~")):
        return None
    if "/" in base:
        if not (repo.repo_path(base) or base.endswith(KNOWN_EXT)):
            return None  # a model id or a route, not a repo path
        return None if repo.has_path(base) else f"path `{tok}` does not exist"
    if base in GENERATED or not base.endswith(KNOWN_EXT):
        return None
    return None if repo.by_name(base) else f"file `{base}` is not in the repository"


def check_doc(path, rel, root, repo, harness, cmd_cache, known, problems):
    lines = path.read_text(errors="replace").splitlines()
    in_fence = False
    for i, line in enumerate(lines, 1):
        if line.lstrip().startswith("```"):
            in_fence = not in_fence
            continue
        # commands: whole line inside a fence, otherwise only inside code spans
        scopes = [line] if in_fence else [m.group(1) for m in CODE_RX.finditer(line)]
        for scope in scopes:
            for m in CMD_RX.finditer(scope):
                cmd, rest = m.group(1), m.group(2)
                if cmd not in known:
                    problems.append(f"{rel}:{i}: `harness {cmd}` is not a command")
                    continue
                flags = help_flags(harness, cmd, cmd_cache)
                if flags is None:
                    problems.append(f"{rel}:{i}: `harness {cmd} -h` gave no usage")
                    continue
                for f in FLAG_RX.findall(rest):
                    if f not in flags:
                        problems.append(f"{rel}:{i}: `harness {cmd}` has no flag -{f}")
        if in_fence:
            continue
        for m in CODE_RX.finditer(line):
            msg = check_path_token(m.group(1).strip(), repo)
            if msg:
                problems.append(f"{rel}:{i}: {msg}")
        for m in LINE_RX.finditer(line):
            fp, n = m.group(1), int(m.group(2))
            if "/" in fp:
                cands = [fp] if (root / fp).is_file() else []
            else:
                cands = repo.by_name(fp)
            if not cands:
                if fp not in GENERATED:
                    problems.append(f"{rel}:{i}: `{fp}:{n}` names a file that does not exist")
                continue
            counts = [sum(1 for _ in (root / c).open(errors="replace")) for c in cands]
            if not any(1 <= n <= c for c in counts):
                problems.append(f"{rel}:{i}: `{fp}:{n}` is past the end of the file ({max(counts)} lines)")


def default_docs(root):
    docs = [root / "README.md", root / "ARCHITECTURE.md", root / "HANDOFF.md"]
    docs += [pathlib.Path(p) for p in sorted(glob.glob(str(root / "docs" / "*.md")))]
    return [d for d in docs if d.is_file()]


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", default=".", help="repository root")
    ap.add_argument("--harness", default=None, help="harness binary (default <root>/bin/harness)")
    ap.add_argument("docs", nargs="*", help="documents to check (default: the project docs)")
    args = ap.parse_args(argv)
    root = pathlib.Path(args.root).resolve()
    harness = args.harness or str(root / "bin" / "harness")
    if not pathlib.Path(harness).is_file():
        print(f"harness binary not found at {harness}; build it first", file=sys.stderr)
        return 2
    docs = [pathlib.Path(d).resolve() for d in args.docs] if args.docs else default_docs(root)
    repo, cmd_cache, problems = Repo(root), {}, []
    known = known_commands(harness)
    for d in docs:
        rel = d.relative_to(root).as_posix() if root in d.parents else d.as_posix()
        check_doc(d, rel, root, repo, harness, cmd_cache, known, problems)
    for p in problems:
        print(p)
    print(f"checked {len(docs)} document(s); {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
