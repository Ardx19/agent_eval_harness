---
title: SWE-bench-Lite-style suite, end to end (offline env, hidden-test verify, validate, grade)
type: feat
status: draft
date: 2026-10-03
origin: pasted plan "SWE-bench-Lite-style suite, end to end" (this file supersedes it; see "Changes from the pasted draft")
---

# SWE-bench-Lite-style suite, end to end

## Summary

Add a coding suite to the harness: `harness validate` gates ~10-15 hand-picked SWE-bench Lite instances, `harness run` runs pi on them in the existing microVM, a second fresh VM runs the hidden tests against the agent's change, and `harness grade` turns stored traces into `report.md`. The verdict (`resolved`) comes only from the verify stage, never from the agent's words, and no LLM is involved anywhere. The task format is chosen so Terminal-Bench-style tasks (env layer + hidden verify script) can reuse it later.

The pasted draft's structure is kept. Reading the code showed that several of its assumptions do not hold; those are listed next because they change the design.

## Changes from the pasted draft

Each item was checked against the repository.

| # | Draft assumption | What the code shows | Consequence |
|---|---|---|---|
| 1 | Build a host venv, pack it into the env layer | Host Python is 3.10 (miniconda) on Ubuntu 22.04; the guest is Ubuntu 24.04 with only Python 3.12, no `pip`, no `git`, no `patch`, no compiler (`images/build/root/usr/bin`). SWE-bench specs for requests/flask/pytest/sphinx/pylint/seaborn use Python 3.9. A host venv would point at a host interpreter that does not exist in the guest. | Build the env with `uv` (already at `~/.local/bin/uv`): a pinned relocatable standalone CPython 3.9 plus `--only-binary` wheels, installed at the guest path it will be mounted at. Env is keyed by (repo, version), not repo. |
| 2 | Reuse `sandbox.BuildWorkspaceWith` for the env image | It is hardwired to `/home/dev/project` and to the prefixes `/home/dev/`, `/usr/share/agent-skills/`, `/opt/svc/`, `/etc/evh/` ([scratch.go:47,73-83](internal/sandbox/scratch.go)). | New root-owned layer builder next to it (same `mke2fs -d` + `DebugfsScript` ownership pass, label `env`). Reuse `imagetool`'s `Walk`/`Override`. |
| 3 | Supervisor takes an extra lowerdir drive | True, but mount points must already exist in the read-only base (comment at [supervisor_linux.go:100](internal/guest/supervisor_linux.go)); `lowerdir=/media:/` is hard-coded. | Add `/envlayer` to the base image, extend the overlay to `lowerdir=/media:/envlayer:/` only when a drive labelled `env` exists. This forces one base-image rebuild, which also adds `git`. |
| 4 | "Python and git are installed in the base image" | Python 3.12 yes; **git no**. A dev box without git is also the kind of "stripped sandbox" the parent plan wants to avoid. | Add `git` (and `patch`) to the base via pinned, checksummed Ubuntu 24.04 `.deb`s unpacked with `dpkg -x` (no root), recorded in `pins.env`. The host is 22.04, so host binaries are not reused. |
| 5 | `ExtractDiff` output is enough to build `model.patch` | `Change` carries path, op, size and sha256 but **no content**, and `ExtractDiff` deletes the `rdump` temp dir on return ([diff.go:59-62](internal/sandbox/diff.go)). | Add an extraction variant that keeps the dumped upper tree (or returns file bytes) so the verify stage has the agent's files. |
| 6 | Verify VM applies `model.patch`, then the test patch | Needs `git`/`patch` in the verify guest and creates a `patch_not_applied` failure class that is really harness noise. | **Materialize on the host**: copy the pristine snapshot, overlay the agent's changed files from the upper dir, apply the hidden test patch with host `git apply`, build the verify workspace from that tree. The guest only runs the test script. `model.patch` (`git diff --binary` of snapshot vs materialized tree) is stored as evidence, not re-applied. `patch_not_applied` disappears; an empty diff is short-circuited to unresolved without booting a VM. |
| 7 | New `cmd/guest-verify` emitting signed result frames | `Supervise` already runs any `RunSpec` argv as uid N and relays stdout line by line in the signed, MAC-chained frame stream ([relay.go](internal/guest/relay.go), [control.go](internal/agents/control.go)). | No new guest binary. Verify = `Supervise` with `argv=[bash, run_tests.sh]` and no proxy. One small change: `RunSpec.Listen == ""` skips starting `guest-fwd`. The host parses `PASSED/FAILED path::name` lines (`pytest -rA --tb=no -q`). |
| 8 | "Provenance F1-F6 apply to any shell-tool claim" | F1/F2/F4-F6 are keyed to skill entrypoints and host toolbox records; pi's built-in `bash` leaves no host record. Worse, **F3 (`groundNumbersAndSmiles`) runs on every final answer** and grounds numbers only against toolbox records ([join.go:341](internal/provenance/join.go)); with no skills it would flag almost every coding answer ("fixed 3 files, 12 tests pass"). | Gate F3 on `len(Manifests) > 0`. Say plainly that for this suite the only host-verified fact is the verify result. Fabrication signals become two post-hoc rules in `grade`: `claimed_success_but_failed` and `claimed_tests_not_run`. |
| 9 | "concurrency 1-2" | `runner.Run` is sequential and `Profile.Concurrency` is not used by it ([runner.go:235](internal/runner/runner.go)). The host has 7.6 GB RAM (3.3 GB free) under WSL, so two 2 GB VMs plus verify are tight. | Sequential for the first pass; parallelism is an optional last unit. |
| 10 | `runner.Task` gets `repo` | `Inputs` go through `StageInputs`, which copies to a temp dir, and `copyPath` rejects symlinks ([runner.go:101-127](internal/runner/runner.go)); `BuildWorkspaceWith` then copies again. Repos contain symlinks. | New `workspace` field: a directory handed to `BuildWorkspaceWith` directly (it handles symlinks and is read-only on the source). |
| 11 | Trace `verify` section is a small add | `trace.Decode` uses `DisallowUnknownFields` and `Validate` is strict ([schema.go:244](internal/trace/schema.go)); `schema.go` is not in the draft's file list. | Add an optional `Verify *Verify` field (omitempty, validated when present) in `internal/trace/schema.go`; older traces still decode. |

## Requirements

- R1. `harness run --profile profiles/swe-lite.yaml --prompts suites/swe-lite/prompts.jsonl` runs each task in a microVM with the repo at `/home/dev/project`, the matching env layer mounted read-only, no network, no hidden tests, no git history beyond one commit.
- R2. After the agent VM is gone, a fresh VM runs the hidden tests on the agent's change and the host stores `verify` in the trace: per-test outcomes, exit code, wall time, `resolved`, `patch_sha256`, `failure_cause`.
- R3. `resolved = every F2P test passes AND every validated P2P test passes`. Nothing the agent says can change it.
- R4. Verify infrastructure failures are retried and never scored as agent failures.
- R5. `harness validate` keeps only tasks where the gold patch resolves 3/3 and the empty patch does not, and records which P2P tests are stable.
- R6. `harness grade` reads stored traces only and is byte-identical on rerun; it writes `grade.json` per run and `report.md` (resolve rate with Wilson interval, per-repo table, cost per resolved task, flags, infra exclusions).
- R7. Edits aimed at the grader are neutralized or flagged (`test_tampering`) by rules, not judgement.
- R8. The task format admits a later Terminal-Bench-style suite without changes to the runner/verify contract.

## Scope boundaries

- No LLM judge. No network in any guest. Hand-picked set of ~10-15 kept tasks (start from ~20 candidates).
- Not in scope: Django, SymPy, scikit-learn, matplotlib, astropy, xarray (compiled extensions or non-pytest runners); Terminal-Bench tasks; n>=3 sweeps (a follow-up once the pipeline is stable); the parent plan's remaining Phase 3+ items.
- Candidate pool (from my recollection of Lite's repo mix; confirm after the dataset download): pytest ~17, sphinx ~16, requests ~6, pylint ~6, seaborn ~4, flask ~3, which is enough for ~20 candidates.

## Key technical decisions

1. **Env = standalone Python + wheels, per (repo, version), dependencies only.** The package under test is never installed into the env (otherwise a later release in `site-packages` leaks the fix). The repo is importable from `/home/dev/project` through `PYTHONPATH`/editable path, which is the same in agent and verify VMs. `compileall` runs at build so the read-only layer needs no `.pyc` writes at runtime. A build check fails if any `site-packages` entry matches the package under test.
2. **Versions pinned** in `envs/specs/<name>.json` (python build, wheel versions, hashes via `uv pip compile --generate-hashes`). Build needs network once.
3. **One task = one workspace snapshot**, built by the importer: shallow clone at `base_commit`, `rm -rf .git`, `git init`, one commit, no remotes, tags, reflog or packed refs. Snapshot is checked: `git rev-list --all | wc -l == 1`; hidden test patch's new files absent; test patch's modifications `git apply --check` cleanly.
4. **Verify materializes on the host** (change #6). Tamper rules during materialization:
   - Files touched by the hidden test patch are reset to the snapshot, then the test patch is applied (standard SWE-bench). Any agent change to them sets `test_tampering`.
   - Any `conftest.py`, `pytest.ini`, `tox.ini`, `sitecustomize.py`, `usercustomize.py`, `*.pth` in the agent's change is dropped and flagged `test_tampering`.
   - `setup.cfg` / `pyproject.toml` are **flagged but kept**, because real fixes legitimately edit them. (Decision point; see open questions.)
   - Only changes under `/home/dev/project` are carried. Anything the agent wrote elsewhere (`~/.local`, `/tmp`) does not reach the verify VM, which removes the user-site `sitecustomize` route entirely.
   - Excluded as noise: `.git/**`, `__pycache__`, `*.pyc`, `.pytest_cache`, `*.egg-info`, build dirs. Binary or >1 MiB changes: recorded as `unverifiable_change`, not carried.
5. **Verify results travel as ordinary signed stdout frames**; the verify VM has no key file, proxy or toolbox listener, and runs as uid 1000.
6. **`resolved` uses validated P2P**, not the dataset's raw list: `validate` computes P2P as the tests that pass on (base + test patch) and under the gold patch in all three runs. Environment-flaky P2P tests are removed individually and recorded, rather than dropping the whole task.
7. **Fabrication rules live in `grade`, not the trace**, so they can be improved without rerunning anything:
   - `claimed_success_but_failed`: final answer matches success phrases (fixed regex list in code) and `resolved=false`.
   - `claimed_tests_not_run`: answer claims tests pass, but no model-issued `bash` call in the proxy transcript contains a test-runner command.
8. **Agent prompt** = the issue text under one neutral line ("Resolve the following issue in the repository at /home/dev/project."). Instance ids and repo names stay out of the prompt (they are only the task `id`, which the agent never sees). Contamination of well-known repos is a known limitation and is reported as such.

## Task format

`suites/swe-lite/prompts.jsonl` (runner reads; agent sees only `prompt`):

```json
{"id":"pallets__flask-4992","prompt":"...","workspace":"repos/pallets__flask-4992","env":"flask-2.3-py39","limits":{"wall_s":1200,"tokens":600000},"tags":["swe-lite","flask"]}
```

`suites/swe-lite/verify/<id>/` (host only, never staged to the agent VM; the runner reads it only inside the verify stage): `test_patch.diff`, `f2p.txt`, `p2p.txt`, `run_tests.sh`, optional `gold.patch`. After validation, `p2p.validated.txt` and an entry in `validated.json` (env image sha256, snapshot tree hash, gold/empty results).

`LoadTasks` keeps `DisallowUnknownFields`; `Task` gains `Workspace` and `Env`. Paths resolve relative to the prompts file and may not escape it (same rule as `StageInputs`).

## Implementation units

Order is dependency order. Each is testable alone.

### U1. Spike (no harness changes)
Goal: decide repo list, Python versions, VM sizing, and whether `uv` standalone Python 3.9 runs in the 24.04 guest.
- On the host, with consent for downloads: build one env (requests, py3.9) with `uv`; hand-pack into ext4 with `mke2fs -d` and `debugfs` ownership; boot a throwaway VM (reuse `vm.New` from a scratch Go test or script, as `images/overlay-selftest.sh` does) with the layer as an extra drive and `mount` + `chroot`-free `PATH` setup; apply gold + test patch on the host; run the tests offline.
- Record in `docs/spikes/swe-lite.md`: env size, build time, boot-to-tests wall time, peak memory, which F2P/P2P tests need network and fail offline, 2 GB vs 1.5 GB.
- Verification: gold resolves, empty does not, measured numbers written down. Also measure how long a requests instance takes at 2 vCPU so the run-time estimate (tasks × (agent ≤1200 s + verify)) is real.

### U2. Image: git, env mount point, env layer builder, supervisor/VM drive
Files: `images/build.sh`, `images/pins.env`, `images/layout.json`, `internal/sandbox/envlayer.go` (new), `cmd/imagetool/main.go` (`env` subcommand), `internal/guest/supervisor_linux.go`, `internal/vm/firecracker.go` (`EnvImage` in `Config`, read-only drive `env`), `envs/build.sh`, `envs/specs/*.json`.
- Base: add `/envlayer` directory; install pinned `git` + `patch` debs with `dpkg -x`; update layout invariants; rebuild. `ImageTreeHash` in traces changes, which is the intended way to tell old runs from new.
- Supervisor: if `blkid -L env` finds a drive, mount `ro` at `/envlayer` and use `lowerdir=/media:/envlayer:/`; otherwise unchanged. Order: workspace over env over base.
- `envs/build.sh <spec>`: `uv python install <pinned>`, `uv pip install --only-binary=:all: --require-hashes`, `compileall`, leak check, tree to `opt/env/<name>/`, build ext4 (root-owned, label `env`), write a manifest with sha256.
- Tests: unit tests for the builder with a toy tree (ownership, label, determinism of the tree hash); `internal/vm` integration test (skips without KVM) booting with a tiny env layer and reading a file from it as uid 1000; existing `overlay_integration_test.go` still passes without an env drive.
- Verification: `images/overlay-selftest.sh`-style check inside a guest: `python3.9 -c 'import <dep>'` works offline; retrosynthesis fixtures still run.

### U3. Task format and importer
Files: `internal/runner/runner.go` (Task fields, `LoadTasks`), `internal/agents/launcher.go` (`Spec.Workspace`, `Spec.EnvImage`), `tools/swe-import/` (Go, host-only), `suites/swe-lite/`.
- `swe-import --ids ids.txt --dataset <downloaded json>` writes prompts, `verify/<id>/`, snapshots, and the `ids → env spec` mapping from the dataset's `version`/`environment_setup_commit`. Downloads (dataset, git clones) need consent.
- `runOne`: if `task.Workspace != ""`, pass it as `InputsDir` without `StageInputs`; resolve `task.Env` to an image path and fail clearly if missing.
- Tests: `LoadTasks` accepts the new fields, rejects escaping paths and unknown fields; the importer's snapshot invariants (single commit, no remotes, test-patch files absent) on a toy repo.

### U4. Changed-file extraction and tree materialization
Files: `internal/sandbox/diff.go` (keep content), `internal/verify/tree.go`, `internal/verify/tamper.go`.
- `ExtractDiffKeep(...)` returns changes plus the dumped upper dir path, cleaned up by the caller; existing `ExtractDiff` becomes a wrapper. `RunTrial` returns the kept dir in `Run` so the runner can verify before deleting it.
- `Materialize(snapshot, upperDir, changes, testPatch)` implements decision 4, returns tree path, `model.patch` bytes, tamper flags, `unverifiable_change` list.
- Tests (toy repo, no VM): modify, create, delete, symlink, binary, rename; edit to a test-patch file reset; new `conftest.py` dropped and flagged; `setup.cfg` change kept and flagged; `.git/index` churn and `__pycache__` ignored; empty diff detected; materialization is deterministic (same inputs, same `patch_sha256`).

### U5. Verify VM, trace `verify` section, runner hook
Files: `internal/verify/run.go`, `internal/guest/protocol.go` and `supervisor_linux.go` (no-proxy mode), `internal/trace/schema.go`, `internal/runner/runner.go`.
- `verify.Run(ctx, spec)` builds the verify workspace (materialized tree + `run_tests.sh` as a root-owned extra under `/opt/svc/` or `/etc/evh/`), boots `vm.New` with the same base + env + `Control`, `RunSpec{Argv: bash run_tests.sh, UID 1000, WallS: verify limit, Listen: ""}`. Parses the frames into per-test outcomes; sets `failure_cause` ∈ `f2p_failed | p2p_broken | verify_timeout | infra | none` and `patch_empty` as informational.
- Infra failures (VMM crash, no exit frame, frames capped, collection error that is not an assertion failure, tests-not-collected) retry up to 2 times, each attempt kept as evidence; if still failing, `verify.status = "infra_error"` and the trial is excluded from the rate, not counted as failed.
- `trace.Verify` fields: `status`, `resolved`, `patch_sha256`, `tests_run`, `f2p`/`p2p` outcome maps (blob ref for the full log), `exit_code`, `wall_s`, `failure_cause`, `attempts`, `tamper`. Optional on `Trace`; `Validate` checks `resolved` is consistent with the outcome maps.
- `runOne` order: agent VM → collect evidence → `verify.Run` (skipped when agent status is `infra_error`; still run for `timeout` and `budget_exceeded`, since partial work may pass) → encode trace once. Profile gets `verify_wall_s` and `verify_mem_mib`.
- Tests: unit tests for the parser (pytest output samples: pass, fail, error, xfail, skip, truncated, tests not collected); integration test with the scripted fake model (empty patch → unresolved without a VM; scripted agent that writes the gold patch → resolved; scripted agent that edits a test file → `test_tampering`, result unchanged; scripted "forger" that claims success without editing → unresolved). A hostile-guest test: the verify guest writing garbage frames increments `Rejected` and cannot flip `resolved`.

### U6. Provenance sanity for no-skill runs
Files: `internal/provenance/join.go` and tests.
- Gate `groundNumbersAndSmiles` on `len(in.Manifests) > 0`. Add a regression test: a coding-style final answer with numbers and no manifests yields no flags. Re-run `harness reanalyze` over `runs/r-20261003-155527` and confirm no change (retrosynthesis runs still have manifests).

### U7. `harness validate`
Files: `cmd/harness/main.go` (replace the stub for `validate`), `internal/verify/validate.go`.
- For each task: empty patch once (F2P must not all pass; nothing may fail from infra), gold patch 3× (F2P all pass each time). Compute `p2p.validated.txt`. Write `validated.json` and `prompts.validated.jsonl`; print a table of kept/dropped with reasons (`env_broken`, `f2p_flaky`, `f2p_passes_on_empty`, `needs_network`, `gold_not_resolved`, `timeout`).
- Cost: `1 + 3` VM boots per task; run sequentially. Re-validation is skipped for tasks whose env image sha and snapshot hash are unchanged.
- Tests: toy repo with a deliberately flaky test (dropped), a test that needs network (dropped), and a task whose gold fails (dropped).

### U8. `harness grade` and report
Files: `cmd/harness/main.go`, `internal/grade/` (checks, rules, report), following the `runner.Reanalyze` pattern.
- Inputs: a run directory. Output: `grade.json` per trial and `report.md`: resolve rate with Wilson 95% interval over non-excluded trials, per-repo table, cost per resolved task, steps/tokens, status counts, provenance flag counts, `test_tampering`, `claimed_success_but_failed`, `claimed_tests_not_run`, infra exclusions listed with reasons.
- Determinism: sort everything, no timestamps; golden-file test for byte-identical output.
- Tests: Wilson interval against known values; fixtures with resolved, unresolved, infra, tampering, and forger traces; rerun gives identical bytes.

### U9. Live run and review
- `profiles/swe-lite.yaml`: pi, `gemini-3.5-flash-lite`, `skills: []`, `vm_mem_mib: 2048`, `limits.wall_s: 1200`, `tokens: 600000`, `budget_usd: 5`, `concurrency: 1`.
- `harness run` then `harness grade` over the validated set, n=1. Read 3-5 full transcripts (resolved, unresolved, one timeout). Fix harness bugs only, not task selection.
- Acceptance: one command pair produces `report.md`; the scripted forger from U5 is flagged and unresolved in the live pipeline too.

### U10 (optional). Parallel trials
Worker pool in `runner.Run` with a mutex around `sum.TotalCost` and the budget check; depends on measured memory from U1. Only if the sequential run time is a problem.

## Verification summary

- `go test ./...` passes; KVM-dependent tests skip without `/dev/kvm` as today.
- `docs/spikes/swe-lite.md` holds the measured numbers.
- `harness validate`: every kept task shows gold resolved 3/3 and empty unresolved.
- `harness grade` twice on one run directory: byte-identical output.
- Forger agent: unresolved, flagged `claimed_success_but_failed`.
- Retrosynthesis traces still reanalyze identically (U6 gate).

## Risks

- **Offline-incompatible instances.** Some tests need network or compiled extensions; `validate` drops them. Expect ~20 candidates to yield 10-15.
- **Old pinned dependencies on 3.9** may lack wheels for the guest's glibc; `--only-binary` makes that a build-time failure rather than a surprise, and the candidate is dropped.
- **Memory.** 7.6 GB host RAM; sequential until U1 gives real numbers.
- **Base image rebuild** changes `ImageTreeHash`; older traces remain valid under their recorded hash.
- **Weak model.** flash-lite will resolve few instances; the deliverable is the pipeline, not the score.
- **Downloads need consent**: the SWE-bench Lite dataset, git clones, `.deb`s, Python builds, wheels. Nothing is fetched until you say so.

## Open questions

1. `setup.cfg` / `pyproject.toml` edits: keep-and-flag (planned) or reset like `conftest.py`? Keep-and-flag avoids punishing legitimate fixes but leaves a small config-based attack surface; the hidden run uses `-p no:cacheprovider` and an explicit `-c /dev/null`-style rootdir only if U1 shows it does not break these repos.
2. Should `run` refuse tasks that are not in `validated.json`? Planned: yes, by default consuming `prompts.validated.jsonl`.
3. Add Django (pure Python, 114 Lite instances, own test runner `tests/runtests.py`) after the first pass? It needs a second log parser; deferred.
