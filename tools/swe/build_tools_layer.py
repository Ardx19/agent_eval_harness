#!/usr/bin/env python3
"""Build envs/build/<name>/env.ext4 holding command-line tools the guest image lacks.

    tools/swe/build_tools_layer.py tbtools git jq sqlite3 make patch

Packages are resolved against Ubuntu noble's index and against what the base image
already has (its dpkg status file), so only missing packages are added. Needs
network on the HOST only. The layer is read-only and root-owned in the guest.
"""
import gzip, pathlib, re, shutil, subprocess, sys, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE_STATUS = ROOT / "images/build/root/var/lib/dpkg/status"
MIRROR = "http://archive.ubuntu.com/ubuntu"
SUITES = [("noble", "main"), ("noble", "universe"), ("noble-updates", "main"), ("noble-updates", "universe"), ("noble-security", "main"), ("noble-security", "universe")]
# heavy optional dependencies that the tools do not need for the jobs tasks give them
SKIP = {"libcurl3t64-gnutls", "liberror-perl", "git-man", "ca-certificates", "openssh-client", "less", "patch-doc"}


def stanzas(text):
    for block in text.split("\n\n"):
        d, key = {}, None
        for line in block.splitlines():
            if line.startswith((" ", "\t")):
                if key:
                    d[key] += "\n" + line
            elif ":" in line:
                key, v = line.split(":", 1)
                d[key] = v.strip()
        if "Package" in d:
            yield d


def vkey(v):
    return [int(x) if x.isdigit() else x for x in re.split(r"(\d+)", v)]


def load_index():
    best = {}
    provides = {}
    for suite, comp in SUITES:
        url = f"{MIRROR}/dists/{suite}/{comp}/binary-amd64/Packages.gz"
        data = gzip.decompress(urllib.request.urlopen(url, timeout=120).read()).decode()
        for d in stanzas(data):
            n = d["Package"]
            if n not in best or vkey(d["Version"]) > vkey(best[n]["Version"]):
                best[n] = d
    for n, d in best.items():
        for p in re.split(r",\s*", d.get("Provides", "")):
            p = p.split("(")[0].strip()
            if p:
                provides.setdefault(p, []).append(n)
    return best, provides


# The CI base image ships no dpkg database, so "already installed" is decided by
# what is on disk: a fixed list of core packages, plus any library whose shared
# object is present in the image.
CORE = {"libc6", "libc-bin", "libgcc-s1", "gcc-14-base", "libstdc++6", "perl", "perl-base", "perl-modules-5.38", "libperl5.38t64", "dpkg",
        "debconf", "readline-common", "zlib1g", "libpcre2-8-0", "libreadline8t64", "libselinux1", "libacl1", "libattr1", "libtinfo6", "libssl3t64", "libexpat1", "coreutils", "sed", "grep", "tar", "bash", "dash", "findutils", "diffutils", "base-files", "libgcc1"}
LIBDIR = ROOT / "images/build/root/usr/lib/x86_64-linux-gnu"


def base_has(pkg):
    if pkg in CORE:
        return True
    if pkg.startswith("lib"):
        core = re.sub(r"(t64)?[\d.\-]*$", "", pkg)[3:]
        return bool(core) and any(LIBDIR.glob(f"lib{core}.so*"))
    return False


class _Have:
    def __contains__(self, n):
        return base_has(n)


def installed():
    if not LIBDIR.is_dir():
        sys.exit("build the base image first (images/build.sh): " + str(LIBDIR))
    return _Have()


def closure(want, best, provides, have):
    out, todo = [], list(want)
    seen = set(want)
    while todo:
        n = todo.pop()
        if n not in best:
            n2 = (provides.get(n) or [None])[0]
            if not n2:
                sys.exit(f"package {n} not found in the index")
            n = n2
        out.append(n)
        deps = ", ".join(x for x in (best[n].get("Depends", ""), best[n].get("Pre-Depends", "")) if x)
        for group in filter(None, re.split(r",\s*", deps)):
            alts = [re.sub(r"\s*\(.*?\)|:any", "", a).strip() for a in group.split("|")]
            if any(a in have or a in seen for a in alts):
                continue
            pick = next((a for a in alts if a in best or a in provides), None)
            if pick is None or pick in SKIP:
                continue
            seen.add(pick)
            todo.append(pick)
    return out


def fetch(filename):
    """Download a .deb, trying the main mirror and then the security mirror."""
    last = None
    for host in (MIRROR, "http://security.ubuntu.com/ubuntu", "http://azure.archive.ubuntu.com/ubuntu"):
        try:
            return urllib.request.urlopen(f"{host}/{filename}", timeout=300).read()
        except Exception as e:  # noqa: BLE001
            last = e
    sys.exit(f"cannot download {filename}: {last}")


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    name, want = sys.argv[1], sys.argv[2:]
    out = ROOT / "envs/build" / name
    tree = out / "tree"
    shutil.rmtree(tree, ignore_errors=True)
    tree.mkdir(parents=True)
    best, provides = load_index()
    pkgs = closure(want, best, provides, installed())
    print("packages:", " ".join(sorted(pkgs)))
    for n in sorted(set(pkgs)):
        deb = out / f"{n}.deb"
        deb.write_bytes(fetch(best[n]["Filename"]))
        subprocess.run(["dpkg-deb", "-x", str(deb), str(tree)], check=True)
        deb.unlink()
    for rel in ("usr/share/doc", "usr/share/man", "usr/share/locale", "usr/share/info", "usr/share/bash-completion", "usr/share/lintian"):
        shutil.rmtree(tree / rel, ignore_errors=True)
    # the guest has /bin -> usr/bin; packages that ship /bin or /sbin files must follow
    for d in ("bin", "sbin", "lib", "lib64"):
        p = tree / d
        if p.is_dir() and not p.is_symlink():
            tgt = tree / "usr" / d
            tgt.mkdir(parents=True, exist_ok=True)
            for f in p.iterdir():
                shutil.move(str(f), str(tgt / f.name))
            p.rmdir()
    subprocess.run([str(ROOT / "bin/layertool"), "--src", str(tree), "--out", str(out / "env.ext4"), "--label", "env"], check=True)
    shutil.rmtree(tree)
    print("ready:", out / "env.ext4")


if __name__ == "__main__":
    main()
