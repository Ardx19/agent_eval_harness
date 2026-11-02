#!/usr/bin/env python3
"""Build one SWE-bench-Lite task offline-ready: a pristine single-commit repo,
a relocatable Python toolchain layer, and the host-only verify files.

    tools/swe/build_env.py pytest-dev__pytest-7432

Needs network (GitHub, PyPI, archive.ubuntu.com) on the HOST only. The guest never
gets any. Outputs under envs/build/<id>/:
    repo/            what the agent works in (/home/dev/project): base commit, one commit, no remote
    env.ext4         read-only toolchain layer (/opt/env python + deps, /usr/bin/git)
    env.ext4.entries.jsonl
and under suites/swe-lite/verify/<id>/ (never shown to the agent):
    test_patch.diff  gold.patch  tests.json
"""
import datetime, gzip, io, json, os, pathlib, shutil, subprocess, sys, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
RAW = ROOT / "suites/swe-lite/raw"
UV = os.path.expanduser("~/.local/bin/uv")

# repo -> how to build its environment. pytest-style repos only for now.
def pytest_deps(v):
    v = float(v)
    base = ["attrs", "py", "packaging", "wcwidth", "iniconfig", "pexpect", "mock", "requests", "setuptools<81", "tomli", "exceptiongroup"]
    if v < 6:
        return base + ["pluggy<1.0", "more-itertools", "atomicwrites", "hypothesis<6", "xmlschema<2"]
    if v < 8:
        return base + ["pluggy<2", "hypothesis<6.40", "xmlschema", "attrs"]
    return base + ["pluggy>=1.3", "hypothesis", "xmlschema"]


SPECS = {
    "pytest-dev/pytest": {"deps": pytest_deps},
    "pallets/flask": {"deps": lambda v: ["pytest<8", "asgiref", "blinker", "werkzeug<3", "jinja2<3.2", "itsdangerous<2.2", "click<8.2", "markupsafe<3"]},
    "psf/requests": {"deps": lambda v: ["pytest<8", "httpbin", "pytest-httpbin", "pytest-mock", "urllib3<2", "chardet", "idna", "certifi"]},
}
# SWE-bench "version" -> python minor
PYTHON = {"pytest-dev/pytest": lambda v: "3.9" if float(v) < 8 else "3.11",
          "pallets/flask": lambda v: "3.11", "psf/requests": lambda v: "3.9"}


def run(*a, cwd=None, env=None, check=True):
    r = subprocess.run(a, cwd=cwd, env=env, text=True, capture_output=True)
    if check and r.returncode:
        sys.exit(f"command failed: {' '.join(map(str, a))}\n{r.stdout[-2000:]}\n{r.stderr[-3000:]}")
    return r


def load_row(iid):
    for p in sorted(RAW.glob("rows_*.json")):
        for r in json.load(open(p))["rows"]:
            if r["row"]["instance_id"] == iid:
                return r["row"]
    sys.exit(f"{iid} not in {RAW}")


def make_repo(row, out):
    repo = out / "repo"
    if repo.exists():
        return repo
    tmp = out / "repo.tmp"
    shutil.rmtree(tmp, ignore_errors=True)
    tmp.mkdir(parents=True)
    run("git", "init", "-q", cwd=tmp)
    run("git", "fetch", "-q", "--depth", "1", f"https://github.com/{row['repo']}", row["base_commit"], cwd=tmp)
    run("git", "checkout", "-q", "FETCH_HEAD", cwd=tmp)
    # drop history: the agent must not find the future fix in git objects, tags or reflog
    shutil.rmtree(tmp / ".git")
    run("git", "init", "-q", "-b", "main", cwd=tmp)
    env = dict(os.environ, GIT_AUTHOR_NAME="dev", GIT_AUTHOR_EMAIL="dev@localhost", GIT_COMMITTER_NAME="dev",
               GIT_COMMITTER_EMAIL="dev@localhost", GIT_AUTHOR_DATE="2020-01-01T00:00:00", GIT_COMMITTER_DATE="2020-01-01T00:00:00")
    run("git", "add", "-A", cwd=tmp)
    run("git", "commit", "-q", "-m", "Initial commit", cwd=tmp, env=env)
    tmp.rename(repo)
    return repo


def relocate(env_dir, repo, repo_final="/home/dev/project", env_final="/opt/env"):
    """Rewrite the host build paths to the guest paths in every text file."""
    subs = [(str(repo).encode(), repo_final.encode()), (str(env_dir).encode(), env_final.encode())]
    n = 0
    for base in (env_dir / "venv", repo):
        for p in base.rglob("*"):
            if p.is_symlink() or not p.is_file() or p.stat().st_size > 2_000_000:
                continue
            b = p.read_bytes()
            if b"\0" in b[:4096] or not any(s in b for s, _ in subs):
                continue
            for s, d in subs:
                b = b.replace(s, d)
            p.write_bytes(b)
            n += 1
    # the venv's interpreter symlinks are absolute; make them relative so they resolve under /opt/env
    for name in ("python", "python3", f"python{PYV}"):
        link = env_dir / "venv/bin" / name
        if link.is_symlink():
            link.unlink()
            link.symlink_to(f"../../py/bin/python{PYV}")
    return n


def add_git(tree):
    idx = urllib.request.urlopen("http://archive.ubuntu.com/ubuntu/dists/noble-updates/main/binary-amd64/Packages.gz", timeout=60).read()
    fn = None
    cur = {}
    for line in gzip.decompress(idx).decode().splitlines():
        if not line:
            if cur.get("Package") == "git":
                fn = cur["Filename"]
                break
            cur = {}
        elif ":" in line and not line.startswith(" "):
            k, v = line.split(":", 1)
            cur[k] = v.strip()
    if not fn:
        sys.exit("git deb not found in noble-updates")
    deb = tree.parent / "git.deb"
    deb.write_bytes(urllib.request.urlopen("http://archive.ubuntu.com/ubuntu/" + fn, timeout=120).read())
    run("dpkg-deb", "-x", str(deb), str(tree))
    deb.unlink()
    shutil.rmtree(tree / "usr/share/doc", ignore_errors=True)
    shutil.rmtree(tree / "usr/share/bash-completion", ignore_errors=True)


def main():
    global PYV
    iid = sys.argv[1]
    row = load_row(iid)
    spec = SPECS[row["repo"]]
    PYV = PYTHON[row["repo"]](row["version"])
    out = ROOT / "envs/build" / iid
    out.mkdir(parents=True, exist_ok=True)
    vdir = ROOT / "suites/swe-lite/verify" / iid
    vdir.mkdir(parents=True, exist_ok=True)
    (vdir / "test_patch.diff").write_text(row["test_patch"])
    (vdir / "gold.patch").write_text(row["patch"])
    files = sorted({l.split(" b/", 1)[1] for l in row["test_patch"].splitlines() if l.startswith("diff --git ")})
    (vdir / "tests.json").write_text(json.dumps({
        "instance_id": iid, "repo": row["repo"], "base_commit": row["base_commit"], "python": PYV,
        "test_files": files, "fail_to_pass": json.loads(row["FAIL_TO_PASS"]), "pass_to_pass": json.loads(row["PASS_TO_PASS"]),
    }, indent=1))
    (out / "problem.txt").write_text(row["problem_statement"])

    repo = make_repo(row, out)
    tree = out / "tree"
    envd = tree / "opt/env"
    if not (out / "env.ext4").exists():
        shutil.rmtree(tree, ignore_errors=True)
        envd.mkdir(parents=True)
        run(UV, "python", "install", PYV)
        py = pathlib.Path(run(UV, "python", "find", PYV).stdout.strip()).resolve().parents[1]
        shutil.copytree(py, envd / "py", symlinks=True)
        run(UV, "venv", "-q", "--python", str(envd / f"py/bin/python{PYV}"), str(envd / "venv"))
        base_env = dict(os.environ, SETUPTOOLS_SCM_PRETEND_VERSION=row["version"] + ".0", SETUPTOOLS_SCM_PRETEND_VERSION_FOR_PYTEST=row["version"] + ".0")
        vpy = str(envd / "venv/bin/python")
        # build tools are current; everything the project and its tests need resolves as of the task's date
        run(UV, "pip", "install", "-q", "--python", vpy, "setuptools", "setuptools_scm", "wheel", "toml", env=base_env)
        run(UV, "pip", "install", "-q", "--python", vpy, *spec["deps"](row["version"]), env=base_env)
        run(UV, "pip", "install", "-q", "--python", vpy, "--no-build-isolation", "-e", str(repo), env=base_env)
        print("relocated", relocate(envd, repo), "files")
        add_git(tree)
        # hidden verify test commands and toolchain version are recorded for the harness
        run(str(ROOT / "bin/layertool"), "--src", str(tree), "--out", str(out / "env.ext4"), "--label", "env")
        shutil.rmtree(tree)
    print("ready:", out)


if __name__ == "__main__":
    main()
