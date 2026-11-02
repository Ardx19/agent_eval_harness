---
title: Sandbox-first completion of the agent eval harness (re-gate, robustness, evidence, held-out detector evaluation, docs)
type: feat
status: active
date: 2026-10-10
origin: last_stretch_plan.md
---

# Sandbox-first completion of the agent eval harness

## Summary

Make the sandbox (Firecracker isolation, the signed tool path, the trace and its evidence) provably sound on this machine first. Re-run every gate that is stale, fix what fails with regression tests, harden the failure paths, and check stored evidence and determinism. Then measure the fabrication detectors on a new held-out set scored once, using fresh Gemini traces (`gemini-3.5-flash-lite` via pi). Then write the docs and close out.

The grader (`grader/`), its calibration, the grader-side labels, and the seven not-built features (G1 to G7) are deferred to a later pass. They do not block the sandbox or the detector result.

Revised 2026-10-10 from the earlier plan: sandbox-first ordering, grader calibration and G1 to G7 moved out, DeepSeek removed, Gemini held-out prompts written new and approved before running.

---

## Problem Frame

The harness runs and its Go test suite passes, but three things are not yet established:

1. The sandbox gates were last run on 2026-10-05, before guest-supervisor edits on 2026-10-05. Coding-task validation (`validate` on the SWE and terminal suites, and `tools/tbench/selfcheck.py`) is therefore stale.
2. The failure paths (refusals, hung streams, lost exit frames, tool memory kills) are classified by code that has not been checked against a deliberate test for each case.
3. The fabrication detectors (F1 to F6, U1, U2) have only been checked against forgeries written by the same author. Their accuracy on traces they were not tuned on is unknown.

The user's top priority is detecting fabricated tool use, and the user wants the base to be close to perfect. This plan measures the base against explicit pass criteria and reports what it cannot prove. It does not claim zero fabrication.

---

## Requirements

- R1. All five isolation tests named in `last_stretch_plan.md` section 8 (F4) run, pass, and are not skipped: `TestHostileGuestIsContained`, `TestToolPathAndAttacksInARealGuest`, `TestTwoConcurrentVMsStayIsolated`, `TestRealPiForgerIsCaught`, `TestForwardsWithRealKeyAndStripsGuestCredentials`.
- R2. `harness validate` passes gold 3 of 3 and fails empty on every task in `suites/swe-lite` and `suites/tbench`. `tools/tbench/selfcheck.py` passes after the last guest change.
- R3. Each failure mode (refusal storm, hung request, slow but progressing stream, malformed stream, lost exit frame, tool out-of-memory) is classified by a test that states the expected class before the code runs.
- R4. Stored traces have zero dangling blob references and zero summary count mismatches. The Harbor ATIF validator passes on at least 10 traces across agents and models.
- R5. `harness grade`, `harness reanalyze` and `harness eval-detectors` give byte-identical output on repeat runs.
- R6. Fabrication detectors are scored on a held-out set that no detector fix used, scored once, with Wilson 95% intervals, the miss list, and the false-alarm list.
- R7. No API key is printed, stored in a file, or passed to the guest. Project-owned files and run output contain none.
- R8. `tools/audit/check_docs.py` passes: every command and flag in the docs exists, every referenced path exists, and every `file:line` resolves.
- R9. No live model call starts without the user's go-ahead. Held-out prompts are approved before the held-out run.
- R10. Hashes of the detector code and the harness binary are recorded before the held-out scoring.

---

## Scope Boundaries

- The grader (`grader/`): no calibration, no code changes, no new labels for grader outcomes. Its existing tests still run inside the gates (U2, U8) so a breakage is caught.
- The seven not-built features (G1 pacing, G2 pass^k table, G3 per-call limits, G4 shared toolchain layers, G5 non-Python tasks, G6 exec log, G7 `--allow-web`). Deferred to a later pass.
- Gemini break-tests (`suites/initial`) as a grader-calibration set. Not run in this plan. The smoke run in U10 uses two of its prompts for quota measurement only.
- DeepSeek routes (`profiles/retro-deepseek*.yaml`), mimo and big-pickle break-tests, and the Claude Code adapter (needs an Anthropic key).
- Git initialization. Offered at close-out; done only if the user asks.
- Changing `.wslconfig`. Requires the user's approval.
- Compiled `native` skill runner, prompt-injection and fault-injection task sets, Django SWE tasks.

### Deferred for later

- Grader calibration, the grader change list in `grader/FEEDBACK.md`, and reference labels for grader outcomes.
- G1 to G7 (see Scope Boundaries). Each gets its own go/no-go in the later pass.

### Deferred to Follow-Up Work

- Detector v2 with new fresh data, if the held-out result is weak. Triggered only by the U15 report.

---

## Context & Research

### Relevant Code and Patterns

- Trial lifecycle and hop map: `internal/agents/launcher.go` (`RunTrial`, `classify` near line 340), `internal/runner/runner.go` (`Run`, `runOne`, `runVerify`), `internal/vm/firecracker.go` (`New`, `Start`, `Wait`), `internal/proxy/proxy.go` (`ServeConn`, `serve`), `internal/skills/toolbox.go` (`ServeConn`, `handle`), `internal/guest/supervisor_linux.go` (`Supervise`, `runAgent`).
- Breaker: `internal/runner/runner.go` lines 409 to 465 (`type breaker`, `trip`, `wait`, `refused()` at 459 to 462). Call sites at 356 and 362 to 372. Existing test: `internal/runner/pool_test.go:139` (`TestRefusedTrialsPauseTheRun`). The breaker pauses and never stops.
- Run exit code: `cmd/harness/run.go` lines 97 to 102. A clean early stop exits 0.
- Profile limits: `internal/config/profile.go` lines 13 to 18 (`Limits`: `wall_s`, `idle_s`, `tokens`, `mem_mb`), defaults at 76 to 87. Unknown keys are rejected (`KnownFields(true)`, line 46). The `web` key is parsed but read by no code.
- Verify and integrity: `internal/verify/verify.go` (`BuildTree` at 242, `tampering` at 161), `internal/verify/integrity.go` (`AddCanary` at 21, `CheckIntegrity` at 70).
- Evidence and blobs: `internal/trace/schema.go` (`Encode` 253, `Decode` 269), `internal/trace/blobs.go` (`Store.Put` 31), `internal/trace/atif.go` (`ValidateATIF` 82).
- Detector evaluation: `internal/evaldet/evaldet.go` (`LoadLabels` 207, silent `split = "heldout"` default at 222 to 224, pack writer at 85, key IDs are a hash of the trace path at 55). `internal/grade/grade.go:63` (`Wilson`), not yet used by evaldet.
- Queue: `tools/queue/run_queue.sh` (stage definitions 33 to 45, gemini-rest gated at 48 to 55, report at 57 to 63 runs `rm -rf suites/detector-eval/pack` at 61). `want()` silently skips unknown names.
- Gemini profiles: `profiles/retro-eval.yaml` and `profiles/retro-suite.yaml` (pi, `gemini-3.5-flash-lite`, `wall_s: 900`, `tokens: 500000`, concurrency 1).
- Host readiness, checked read-only on 2026-10-10: WSL2 kernel 5.15.167.4, 12 CPUs, 11.9 GB total and about 8.4 GB available, 144 GB free. `/dev/kvm` writable by the user. `spike/firecracker/vmlinux-6.1.186` and `ubuntu-24.04.squashfs` match `images/pins.env`. `images/build/rootfs.ext4` is present (2048 MiB). `bin/harness` is built (Oct 7). `tools/harbor-venv` is present, and rdkit imports.
- Test inventory: 244 top-level Go test functions across `internal/` and `cmd/`. The four VM-gated tests above run here rather than skip.

### Institutional Learnings

- Retro tool peaks near 5.4 GB. WSL is set to `memory=12GB` in the Windows-side `.wslconfig` (`/mnt/c/Users/Aryan Datt/.wslconfig`). Before any run that may reach that peak (p34), require at least 11 GB available.
- Gemini's free quota is 500 requests per model per day. Successful Gemini trials used 2 to 45 requests (median about 15 to 20). Earlier runs logged 76 `infra_error` trials with reason `upstream`.
- The `ce-sessions` review found that a process-kill command (`pkill -f`) matched its own shell. Kill by PID.
- Reanalysis writes `trace.reanalyzed.json` into stored runs. Determinism checks must run on a scratch copy, not on `runs/`.

### External References

None needed. The decisions here concern this repo's tools and data.

---

## Key Technical Decisions

- **Sandbox first, detectors second.** The detector result is only meaningful if the sandbox that produced the traces passes its gates. Reason: the traces are the evidence, so their collection path must be sound.
- **The gate is a full re-run, with `-count=1`.** Cached results are not accepted. Reason: the last gate predates guest edits.
- **A gate failure becomes a unit with a regression test that fails before the fix.** Reason: the user's standing rule (any defect gets a regression test before it is called fixed).
- **Refusal limit is `limits.max_refusals` in the profile, default 0 (unlimited).** Reason: the existing `limits` block already holds run limits. Default 0 keeps current behaviour and existing tests.
- **Held-out traces go to `runs/detector-eval-heldout/`, and the held-out pack goes to `suites/detector-eval-heldout/pack/`.** Reason: the queue's `rm -rf` on the dev pack path cannot delete held-out labels.
- **Held-out prompts are new and approved before any run.** Reason: the 37 existing detector-eval prompts already appear in six dev runs (checked), so they are not held out.
- **Dev = pack A (66 existing traces). Held-out = new prompts with fresh Gemini traces, scored once.** The other roughly 85 existing traces in `runs/detector-eval` are regression inputs only (no crashes, deterministic output), not scored.
- **Freeze before scoring.** `internal/provenance/*.go` and `bin/harness` are hashed after the dev fixes and before the held-out scoring. The hashes go into `docs/results.md`.
- **Labeller is a model family other than hy3 and Gemini.** Reason: reduces correlated errors. The user picks the exact model.
- **No git commits unless the user asks.** Snapshots are tar archives with sha256 manifests.
- **Stored runs are not mutated by checks.** Determinism and reanalysis checks run on a scratch copy under `runs/gates/`.

---

## Open Questions

### Resolved During Planning

- Scope: sandbox first; grader and G1 to G7 deferred. (User.)
- Dev/held-out split: pack A dev, new Gemini held-out. (User.)
- Held-out prompts: new, approved before running. (User.)
- Gemini replaces DeepSeek. (User.)

### Deferred to Implementation

- The expected classification for each F8 case (U5). Decided by reading `classify` in `internal/agents/launcher.go` and written into the test before the code runs.
- Exact held-out prompt count (target 20 to 25) and trials per prompt (n=1 or n=2). Decided at U11 approval, based on quota left.
- Exact labeller model. The user decides at U14.
- Specific detector fixes in U15. Each depends on dev-only findings and gets a synthetic test.

---

## Implementation Units

Two phases. Phase 1 needs no live model calls. Phase 2 needs live Gemini calls and labels. Phase 3 closes out.

Units that can run in parallel with Phase 1: U11 (held-out prompt drafting), U13 (detector tooling), and U14's dev-pack labelling.

| Unit | Phase | Depends on | Live model calls | Needs user |
|---|---|---|---|---|
| U1 Snapshot and hygiene | 1 | none | no | no |
| U2 Sandbox re-gate | 1 | U1 | no | no |
| U3 Fix gate failures | 1 | U2 (only if failures) | no | only if a fix exceeds 1 day |
| U4 Refusal limit | 1 | U1 | no | no |
| U5 Failure-path robustness | 1 | U3, U4 | no | no |
| U6 Stored evidence and determinism | 1 | U3 | no | no |
| U7 Secrets, docs checker, audit draft | 1 | U5, U6 | no | no |
| U8 Sandbox gate close | 1 | U3 to U7 | no | no |
| U9 Queue hardening | 2 | U1, U4 | no | no |
| U10 Gemini smoke and quota probe | 2 | U8, U9 | yes (small) | go-ahead |
| U11 Held-out prompt drafting | parallel | U1 | no | approval |
| U12 Held-out Gemini run | 2 | U8, U9, U10, U11 | yes (20 to 25 prompts) | go-ahead |
| U13 Detector tooling | parallel | U1 | no | no |
| U14 Labelling | parallel (pack A), 2 (pack B) | U13; pack B needs U12 | no | labels |
| U15 Detector evaluation | 2 | U8, U12, U13, U14 | no | dev-fix review |
| U16 Documentation | 3 | U15 | no | no |
| U17 Close-out | 3 | U16 | no | git-init decision |

---

### U1. Snapshot and hygiene

**Goal:** A restorable snapshot exists before any change, and the stray files are removed.

**Requirements:** R9.

**Dependencies:** none.

**Files:**
- Create: `backups/pre-sandbox-completion.tgz`, `backups/pre-sandbox-completion.sha256`
- Delete (after the snapshot is verified): `suites/initial/.filtered.initial-hy3.jsonl`, `suites/initial/.filtered.initial-mimogo.jsonl`, `suites/initial/.filtered.initial-gemini.jsonl`, `grader-return.zip:Zone.Identifier`

**Approach:**
- Archive `cmd internal grader tools profiles suites docs images/*.sh *.md go.mod go.sum`. Exclude `envs/`, `runs/`, `images/build/`, `bin/`, `backups/`.
- Write the sha256 manifest next to the archive, and verify it before deleting anything.
- The `.filtered.*` files are generated copies made by `run_queue.sh`. They are not user input.

**Test expectation:** none -- filesystem step. Verified by the manifest.

**Verification:**
- `sha256sum -c` on the manifest passes.
- The four listed files are gone. No other file is removed.

---

### U2. Sandbox re-gate

**Goal:** Establish, with logs, the current state of every sandbox gate, before changing anything.

**Requirements:** R1, R2, R5 (baseline).

**Dependencies:** U1.

**Files:**
- Create: `runs/gates/2026-10-xx-baseline/` (logs and a summary file). No code changes in this unit.

**Approach:**
- Preconditions: `free -m` shows at least 11 GB available, no harness process running, Windows sleep off.
- Build `bin/harness` and run `go vet ./...`.
- Run `go test ./... -count=1 -timeout 1500s -v`, saving the log. List every SKIP with its reason.
- Run the five F4 tests by name with `-v` and confirm each shows PASS, not SKIP.
- Run `bin/harness validate` on `suites/swe-lite` (profile `profiles/swe-lite.yaml`) and on `suites/tbench` (the tbench profile in `profiles/`). Save both outputs.
- Run `python3 tools/tbench/selfcheck.py`.
- Run the grader tests (`python3 -m pytest grader/tests -q`), recording the collected count.

**Test expectation:** none -- gate run. Results are recorded and feed U3.

**Verification:**
- Every gate has a saved log.
- A baseline table lists each gate with pass, fail, or skip (with reason).
- Failures are written as numbered items with the exact command and the first failing line. Those items become U3.

---

### U3. Fix gate failures

**Goal:** Each gate failure from U2 is fixed, with a regression test that fails before the fix.

**Requirements:** R1, R2.

**Dependencies:** U2 (only if U2 found failures). If U2 is clean, this unit is skipped and the skip is recorded.

**Files:** determined by each failure. Each fix names its file and test in the commit-free change log in `runs/gates/`.

**Approach:**
- One failure at a time. Reproduce, write the failing test, fix, run the test, then run the affected package.
- A VM-related fix that changes guest code requires `images/build.sh`, then `go build -o bin/harness ./cmd/harness`, then a rerun of the five F4 tests.
- A fix that would take more than 1 day of work stops the unit. Report to the user with the reproduction.

**Test scenarios (per failure, written from the U2 output):**
- Error path: the failing command reproduces before the fix.
- Happy path: the same command passes after the fix.
- Regression: the package's existing tests still pass.

**Verification:** U8 reruns the full gate set and shows the same pass result.

---

### U4. Refusal limit

**Goal:** A run stops cleanly after N consecutive model refusals, instead of pausing for hours.

**Requirements:** R3.

**Dependencies:** U1.

**Files:**
- Modify: `internal/config/profile.go` (add `MaxRefusals` with yaml key `max_refusals` to `Limits`, default 0)
- Modify: `internal/runner/runner.go` (count consecutive refused trials near lines 356 to 372; set `sum.Stopped`)
- Modify: `cmd/harness/run.go` (non-zero exit on a refusal stop, near lines 97 to 102)
- Test: `internal/runner/pool_test.go` (beside `TestRefusedTrialsPauseTheRun`), `internal/config/profile_test.go`

**Approach:**
- Count only trials where `refused()` is true. Any other completed trial resets the count, matching the breaker's existing reset rule.
- At N (and only when N is greater than 0), stop starting trials. Set `sum.Stopped = "stopped early: model API refusing"`. In-flight trials finish.
- Wall-time and budget stops are not refusals.

**Patterns to follow:** existing `sum.Stopped` set at `runner.go:344` (budget) and 349 (interrupt).

**Test scenarios:**
- Happy path: with `max_refusals: 3` and four refused trials in a row (fake upstream), no new trial starts after the third, and the stop reason is recorded.
- Edge case: refused, refused, ok, refused, refused with `max_refusals: 3` does not stop.
- Edge case: `max_refusals` absent or 0 never stops, and `TestRefusedTrialsPauseTheRun` passes unchanged.
- Error path: a budget stop with fewer refusals than the limit records the budget reason.
- Integration: `cmd/harness` exits non-zero on a refusal stop and zero on a clean run.
- Error path: a misspelled key near `max_refusals` fails to load (existing `KnownFields` behaviour).

**Verification:** `go test ./internal/runner/ ./internal/config/ -count=1 -run 'Refus|Pool|Limits'` passes.

---

### U5. Failure-path robustness

**Goal:** Each failure mode is classified by a test that states the expected class first.

**Requirements:** R3.

**Dependencies:** U3, U4.

**Files:**
- Modify or create tests in: `internal/agents/` (launcher and classify), `internal/vm/` (exit classification), `internal/guest/` (exit frame), `internal/proxy/` (stream handling), `internal/runner/` (idle watchdog).
- Modify: `internal/agents/launcher.go` only if a test shows a wrong classification (with the test failing first).

**Approach:**
- For each case below, read `classify` (`internal/agents/launcher.go`, near line 340) and the watchdog code. Write the expected class into the test. Then run it.
- A case that already has a test is not duplicated. Record the existing test name.

**Cases and expected behaviour (to confirm against code before writing the test):**
- Refusal storm: refused trials are `infra_error` with reason `upstream`, and U4 stops the run after N.
- Hung request: no progress for `limits.idle_s` ends the trial as an infrastructure failure, not as an agent failure.
- Slow but progressing stream: bytes arriving under the idle limit do not trigger the watchdog.
- Malformed stream: a non-JSON line is counted, not fatal, and does not crash the parser.
- Lost exit frame: a trial that ends without an exit frame is `infra_error` with reason `no_exit_frame`.
- Tool out-of-memory: a tool killed for memory is infrastructure, not an agent failure (matches `docs/not-performed.md` item 1).

**Test scenarios:** one happy and one error test per case, as listed above.

**Verification:** `go test ./internal/agents/ ./internal/vm/ ./internal/guest/ ./internal/proxy/ ./internal/runner/ -count=1` passes, and the list of new and existing test names is recorded in `docs/audit-report.md` (draft).

---

### U6. Stored evidence and determinism

**Goal:** Stored traces are internally consistent, and the three output-producing commands are deterministic.

**Requirements:** R4, R5.

**Dependencies:** U3.

**Files:**
- Create: `tools/audit/check_runs.py`, `tools/audit/tests/test_check_runs.py`
- Output: `runs/gates/2026-10-xx-determinism/` (scratch copies and logs)

**Approach:**
- `check_runs.py` walks each run directory with `trace.json`. It checks that each file decodes, `schema_version` is set, every `sha256:` reference has a blob whose hash matches, and `summary.json` counts match the trace directories.
- Validate at least 10 traces across agents and models with the Harbor ATIF validator (`tools/harbor-venv`, `harbor.utils.trajectory_validator`).
- Determinism: copy one stored run to `runs/gates/`. Run `harness grade` twice. Run `harness reanalyze` twice (the second must leave the output identical). Run `harness eval-detectors` twice on a small pack. Compare outputs with `cmp`. Also run the grader with two different `PYTHONHASHSEED` values.

**Test scenarios:**
- Happy path: `check_runs.py` passes on a clean fixture run.
- Error path: a fixture with a deleted blob is reported with the trace path and the missing hash.
- Error path: a fixture with a changed blob (hash mismatch) is reported.
- Error path: a fixture where `summary.json` says 5 trials and 4 trace directories exist is reported.
- Integration: running the three commands on the scratch copy twice gives byte-identical output.

**Verification:** zero dangling references or mismatches across all stored runs, or each mismatch is listed with its cause in `docs/audit-report.md`.

---

### U7. Secrets, docs checker, audit draft

**Goal:** No key appears in project-owned files, every documented command and path resolves, and the audit draft lists every gate result.

**Requirements:** R7, R8.

**Dependencies:** U5, U6.

**Files:**
- Create: `tools/audit/check_docs.py`, `tools/audit/tests/test_check_docs.py`
- Create: `docs/audit-report.md` (draft: gate results so far)

**Approach:**
- Secrets scan: search for key patterns (`sk-`, `AIza`, `ghp_`, `AKIA`, `BEGIN ... PRIVATE KEY`) in project-owned files and in `runs/`. Report file names and counts only, never matches. Vendored third-party hits (under `tools/harbor-venv/` and `spike/firecracker/squashfs-root/`) are listed and excluded by path, with the reason. The earlier scan found 0 hits in project-owned files.
- `check_docs.py`: every `harness <command> --flag` in `README.md` and the other docs must appear in `harness <command> -h` output. Every referenced path must exist. Every `file:line` must resolve to a line in that file.

**Test scenarios:**
- Happy path: `check_docs.py` passes on a fixture doc that matches.
- Error path: a flag absent from `-h` is reported.
- Error path: a `file:line` past the end of the file is reported.
- Error path: a missing referenced path is reported.

**Verification:** the secrets scan reports zero hits in project-owned files. `check_docs.py` runs (failures are expected until U16 rewrites the docs, and are recorded).

---

### U8. Sandbox gate close

**Goal:** One complete pass of every sandbox gate after all Phase 1 changes, written up as the sandbox milestone.

**Requirements:** R1 to R5, R7.

**Dependencies:** U3 to U7.

**Live model calls:** none. VM tests run locally.

**Files:**
- Update: `docs/audit-report.md` (sandbox section)
- Output: `runs/gates/2026-10-xx-final/`

**Approach:**
- If U5 or U3 changed guest code, rebuild the image (`images/build.sh`) and `bin/harness`, then run the F4 tests again.
- Rerun U2's full gate set in the same order. Also rerun `go test ./... -count=1 -timeout 1500s -v`.
- Compare with the baseline. Every change from baseline must be explained in `docs/audit-report.md`.

**Verification (sandbox milestone pass criteria):**
- Five F4 tests pass and none are skipped.
- `validate` gold 3 of 3, empty fails, on every task in swe-lite and tbench.
- `selfcheck.py` passes.
- Full Go suite passes with every skip explained.
- Grader tests pass (the count is recorded).
- `check_runs.py` has zero dangling references or mismatches, or each is explained.
- Determinism checks are byte-identical.
- Secrets scan has zero hits in project-owned files.

---

### U9. Queue hardening

**Goal:** The queue cannot run a misspelled stage, cannot use a missing prompt path, and cannot delete held-out labels.

**Requirements:** R9.

**Dependencies:** U1, U4.

**Files:**
- Modify: `tools/queue/run_queue.sh`
- Create: `tools/queue/check_prompts.py`, `tools/queue/tests/test_check_prompts.py`

**Approach:**
- `check_prompts.py` resolves each input path in a prompts file relative to the prompts file, and checks that it exists. It exits non-zero with the stage and the missing path.
- `run_queue.sh` calls it before each stage. An unknown name in `ONLY` stops the script before any trial starts.
- Filtered prompt copies (`SKIP`, `TASKS`) go to `runs/queue/filtered/`, not beside the source prompts.
- New stages: `gemini-smoke` (p35 and p38 from `suites/initial/prompts.jsonl`, profile `retro-eval`, budget 0.5 USD) and `gemini-heldout` (profile `retro-eval`, prompts `suites/detector-eval-heldout/prompts.jsonl`, output `runs/detector-eval-heldout`, budget 2.5 USD). Both wait on `tools/queue/gemini_gate.sh`.
- Per-stage time cap: each stage runs in its own process group, with a timeout (default 7 hours, a script variable).
- The report stage writes to a new directory and never deletes files under a pack.
- A `DRY=1` flag prints the stages that would run.

**Test scenarios:**
- Happy path: a prompts file with existing inputs passes `check_prompts.py`.
- Error path: a missing input makes `check_prompts.py` exit non-zero, naming the stage and path.
- Error path: `ONLY=gemini-typo` makes `run_queue.sh` exit non-zero before any stage runs.
- Edge case: `SKIP=p34` writes the filtered copy to `runs/queue/filtered/`, and the source prompts file is byte-identical afterwards.
- Edge case: a `labels.jsonl` placed in a pack directory survives a report stage run.
- Integration: `ONLY=gemini-heldout` with a gate script forced to fail logs "skipped: quota" and starts no trial (fake gate script).

**Verification:** `bash -n tools/queue/run_queue.sh` passes. `python3 -m pytest tools/queue/tests` passes. `DRY=1` output matches the expected stage list.

---

### U10. Gemini smoke and quota probe

**Goal:** Gemini through pi works on two prompts with no infrastructure errors, and the requests per trial are measured before the held-out run.

**Requirements:** R9.

**Dependencies:** U8, U9.

**Live model calls:** yes, small: two prompts, about 30 to 60 requests in total. **Requires explicit go-ahead.**

**Files:**
- Output: `runs/smoke/` (new trial directories)

**Approach:**
- Run `suites/initial` p35 (largest context, one tool call) and p38 (weak-token routing) on `profiles/retro-eval.yaml`, with `max_refusals: 2` and budget 0.5 USD.
- Check `tools/queue/gemini_gate.sh` before starting. If quota is gone, stop and report the reset time. Do not retry in a loop.
- Record requests per trial (count of lines in `proxy/exchanges.jsonl`) and the largest single request.

**Verification (pass criteria):**
- Both trials are `ok`, with no `infra_error` on any exchange.
- The largest single request takes under 2 minutes.
- Requests per trial are recorded in `docs/audit-report.md`, to size U12 against the quota.

---

### U11. Held-out prompt drafting (approval gate)

**Goal:** Twenty to twenty-five new prompts the detectors have not seen, approved by the user before any run.

**Requirements:** R6, R9.

**Dependencies:** U1. Can run in parallel with Phase 1.

**Live model calls:** none.

**Files:**
- Create: `suites/detector-eval-heldout/prompts.jsonl`
- Create: `suites/detector-eval-heldout/README.md` (selection criteria only)
- Create (host only, never sent to the agent or the labeller): `suites/detector-eval-heldout/key.md` (expected behaviour per prompt)

**Approach:**
- Draft 20 to 25 prompts in the style of `suites/detector-eval/prompts.jsonl`. Mix out-of-scope asks the skill cannot do (binding affinity, exact vendor prices, yields it cannot give, named skills that do not exist, memory of runs never made) with 3 to 5 honest controls the skill can do.
- No prompt repeats or closely paraphrases any of the 37 existing detector-eval and detector-eval-extra prompts. Check by normalized text and by id.
- No prompt asks the model to cheat or fabricate. Plain asks only.
- Show the full list to the user, with the proposed n (1 or 2). Wait for approval.

**Test scenarios:**
- Happy path: the file parses as JSONL, ids are unique, and the count is 20 to 25.
- Error path: a prompt whose normalized text matches an existing prompt fails the check.
- Edge case: at least three honest controls are present.
- Integration: `tools/queue/check_prompts.py` (U9) passes on the file.

**Verification:** the user approves the list in chat. The approval is recorded in `docs/audit-report.md`.

---

### U12. Held-out Gemini run

**Goal:** Fresh Gemini traces on the approved held-out prompts, in a separate directory, to be scored once in U15.

**Requirements:** R6, R9, R10.

**Dependencies:** U8, U9, U10, U11 (approved).

**Live model calls:** yes, 20 to 25 prompts at the approved n. **Requires go-ahead after U11 approval.** Spread over quota days.

**Files:**
- Output: `runs/detector-eval-heldout/` (new)
- Output: `suites/detector-eval-heldout/pack/` (new review pack, from `bin/harness review-pack`)

**Approach:**
- Run the `gemini-heldout` stage through the queue (U9).
- No harness code changes during this stage. A bug found here stops the stage, and the user decides whether to rerun.
- Build the review pack into the held-out pack directory, not the dev pack path.
- Record the hash of the pack's `key.jsonl`. The key stays on this machine and is never sent to the labeller.

**Verification:**
- Every approved prompt has a trial directory, or an infrastructure-error trial recorded in `summary.json`.
- The pack has one review markdown per usable trace and a `key.jsonl`. Their counts match.
- The number of usable trials is recorded. If fewer than 15 fabrications are expected, the report says intervals will be wide.

---

### U13. Detector tooling

**Goal:** Labels cannot silently fall into the wrong split, and the detector report includes intervals.

**Requirements:** R6.

**Dependencies:** U1. Can run in parallel with Phase 1.

**Files:**
- Modify: `internal/evaldet/evaldet.go` (require explicit `split` in `LoadLabels`, lines 207 to 224; add Wilson intervals through `grade.Wilson`; add an overall figure that excludes F6)
- Modify: `internal/evaldet/evaldet_test.go`
- Create: `tools/labelpack/LABELLING_PROMPT.md` (exact label format; states the reviewer must not see detector flags or grader verdicts)

**Approach:**
- `LoadLabels` returns an error naming the line when `split` is missing. The silent `heldout` default is removed.
- Each code and the overall figure get a Wilson interval. A code with fewer than 5 positives prints "too few to estimate" in place of an interval.
- The labelling prompt gives the label fields (`trace`, `label`, `kinds`, `split`) and the rule that the reviewer sees only the prompt, the tool calls with arguments and output, and the final answer.

**Test scenarios:**
- Error path: a label line without `split` makes `LoadLabels` fail, naming the line number.
- Happy path: a pack with 10 dev and 10 held-out labels reports intervals for each split and for the overall figure.
- Edge case: a code with 4 positives prints "too few to estimate" and no interval.
- Edge case: the overall figure with F6 excluded differs from the figure with F6 included only when F6 fires (test both).
- Integration: the review pack output contains none of the strings `FABRICATED`, `honest`, or any `F1` to `F6` code (grep the generated markdown).

**Verification:** `go test ./internal/evaldet/ -count=1` passes.

---

### U14. Labelling

**Goal:** Reference labels for the dev pack (A) and the held-out pack (B), with quality checks.

**Requirements:** R6.

**Dependencies:** U13. Pack A can start immediately. Pack B needs U12.

**Files:**
- Create (labels from the user's labeller): `suites/detector-eval/pack/labels.jsonl` (pack A, split `dev`), `suites/detector-eval-heldout/pack/labels.jsonl` (pack B, split `heldout`)
- Create: `docs/labelling-log.md` (who labelled what, double-label agreement, disputes)

**Approach:**
- The user's labeller, using `tools/labelpack/LABELLING_PROMPT.md`, labels each pack. The agent does not write reference labels.
- Quality control: 20% of each pack is labelled twice in different orders. The agent reads at least 25% of each pack and every trace where the label disagrees with the detector flags.
- Disputes go to the user. If the agent's reading disagrees with the labeller on more than 15% of the reviewed sample, the user relabels the disputed traces.
- Pack B labels are not opened by the agent before U15 scoring.

**Test scenarios:**
- Happy path: each label file covers every trace in its pack, with no missing trial.
- Error path: a label with a missing `split` is rejected (U13).
- Leakage check: `grep` of the held-out pack's review markdown finds none of the verdict strings.

**Verification:** `docs/labelling-log.md` records the double-label agreement and each dispute with its resolution.

---

### U15. Detector evaluation

**Goal:** Dev-only detector fixes, then one scored run on the held-out set, with the report saved.

**Requirements:** R6, R10.

**Dependencies:** U8, U12, U13, U14.

**Files:**
- Modify: `internal/provenance/*.go` (dev-driven fixes only)
- Add tests: `internal/provenance/*_test.go` (one synthetic test per fix)
- Create: `docs/detector-eval-heldout.md`
- Update: `docs/results.md` (freeze manifest)

**Approach:**
- Step 1, dev: `bin/harness eval-detectors --pack suites/detector-eval/pack --split dev`. List every miss and false alarm. Classify each as detector bug, label error, or real ambiguity. Candidate causes from earlier review (to confirm, not assume): F4 reading durations and sizes as arguments, F2 on opencode's truncated-output notice, F1 on documentation of how to invoke a tool, F3 on rounded values and agent-supplied structures, F6 on trailing runtime-warning text.
- Step 2: fix only confirmed detector bugs. Each fix has a synthetic test that fails before the fix and passes after.
- Step 3, freeze: `go test ./... -count=1 -timeout 1500s`, then record sha256 of `internal/provenance/*.go` and `bin/harness` in `docs/results.md`.
- Step 4, held-out (once): `bin/harness reanalyze --run runs/detector-eval-heldout/<run>` on a copy, then `bin/harness eval-detectors --pack suites/detector-eval-heldout/pack --split heldout`. Save to `docs/detector-eval-heldout.md`. No detector edits after this step inside this evaluation.
- Report per code only where at least 5 positives exist. Give the overall figure with and without F6, the dev-to-held-out gap, and the full miss and false-alarm lists.
- If held-out is worse than dev, the report says so. Further fixes wait for new fresh data.

**Test scenarios (per dev fix):**
- Error path: the synthetic trace that caused the false alarm is no longer flagged.
- Edge case: the synthetic trace with a true fabrication is still flagged.
- Regression: all existing `internal/provenance` tests pass.

**Test scenarios (held-out step):**
- Integration: `eval-detectors --split heldout` run twice gives byte-identical output.
- Error path: a held-out label file without `split` is refused.

**Verification:** the held-out evaluation runs once. A second run is recorded as a second scoring event. Freeze hashes in `docs/results.md` match the hashes at evaluation time.

---

### U16. Documentation

**Goal:** Every document matches the code, and `check_docs.py` passes.

**Requirements:** R8.

**Dependencies:** U15.

**Files:**
- Create: `ARCHITECTURE.md` (project root): the 15 `internal/` packages and 7 `cmd/` binaries, the vsock port map, the trial steps, the detector list, and the evidence tiers.
- Rewrite: `README.md` (quick start, every command and flag from `-h`, profile keys from `internal/config/profile.go`, suites, run layout, reading a trace, troubleshooting, limits).
- Create: `docs/results.md` (methodology, detector numbers with intervals, freeze manifests, sandbox gate results, limits).
- Rewrite: `HANDOFF.md` (state, how to resume, what is open, the deferred grader and feature work).
- Update: `docs/not-performed.md` (add Gemini status; mark the grader calibration and G1 to G7 as deferred).
- Update: `grader/FEEDBACK.md`: correct the statement "the delivered code in `grader/` is unmodified". List the five changed files against `grader-return.zip`: `grader/decide.py`, `grader/fabrication.py`, `grader/model.py`, `grader/rules.py`, `grader/textutil.py`.
- Update: `docs/plans/2026-10-03-001-feat-agent-eval-harness-plan.md` status to completed, with a divergences section.

**Test scenarios:**
- Integration: `tools/audit/check_docs.py` passes on all docs.
- Error path: a renamed flag in code makes `check_docs.py` fail.

**Verification:** `check_docs.py` exits 0.

---

### U17. Close-out

**Goal:** Final gates pass, the snapshot is saved, and the user has the deliverables list.

**Requirements:** R1 to R10 (final state).

**Dependencies:** U16.

**Files:**
- Create: `backups/final.tgz`, `backups/final.sha256`
- Update: project memory notes under `~/.claude/projects/-home-aryandutt-home-Aryan-agent-eval-harness/memory/`

**Approach:**
- Rerun the sandbox gate set (U8), `check_runs.py`, `check_docs.py`, and the secrets scan.
- Write the final snapshot and manifest. Verify the manifest.
- Confirm nothing is running (`ps` check).
- Ask whether to run `git init` with a `.gitignore` for `runs/`, `envs/`, `bin/`, `images/build/`, `tools/harbor-venv/` and `backups/`. Do not run it without an answer.

**Verification:**
- Manifest verifies. No process from the harness is running. The secrets scan shows zero project-owned hits.

---

## Effort, Schedule, and Critical Path

Estimates are the agent's own effort, not measured. Machine time is separate.

| Phase | Units | My effort | Machine time | Waits on |
|---|---|---|---|---|
| 1 Sandbox | U1 to U8 | about 3 to 4 days (U3 is the risk: 0.5 to 2 days depending on failures) | U2 about 2 h; U5 and U8 full suite about 25 min each | none |
| Parallel | U11, U13, U14 pack A | about 1 day | none | labels for pack A |
| 2 Detectors | U9, U10, U12, U14 pack B, U15 | about 2 days my effort | U10 minutes; U12 spread over 2 quota days | user go-ahead (U10, U12), labels (U14) |
| 3 Close-out | U16, U17 | about 1.5 days | full suite about 25 min | none |

**Critical path:** U1 → U2 → U3 → U5 → U7 → U8 → U10 → U12 → U14 (pack B) → U15 → U16 → U17.

**Calendar:**
- Fastest realistic: about 1 week (sandbox gate clean, labels back within a day, quota available).
- Likely: about 2 weeks (one failure to fix in U3, quota takes two days, labels take a few days).
- Slow: about 3 weeks (labels slow or a fix in U3 exceeds 1 day).

---

## Risks & Dependencies

| Risk | Mitigation |
|---|---|
| U2 finds sandbox failures | U3 is a unit with its own regression tests. A fix over 1 day stops and reports. |
| Gemini quota (500 requests per model per day) runs out during U12 | Quota gate, U4 refusal limit, requests-per-trial measured in U10 before sizing U12 |
| Few fabricated held-out traces, so intervals are wide | Report intervals and "too few to estimate" per code. The user may choose n=2 at U11 |
| Labels are slow or noisy | Pack A starts during Phase 1. Double-labelling of 20%, agent spot-check of 25%, disputes to the user |
| Detector fixes overfit the dev set | Held-out is scored once after the freeze. Fixes need a synthetic test. The dev-to-held-out gap is reported |
| Guest image changes in U3 or U5 break isolation | Image rebuild, then the F4 tests, in U8 |
| WSL memory, Windows sleep, WSL restart kills a run | 12 GB set. Sleep must be off (user action). The queue resumes from logs under `runs/queue/` |
| p34 needs about 5.4 GB and can kill the host | Run only with at least 11 GB available and concurrency 1. p34 is excluded from U10 and U12 |
| The reanalysis step changes stored runs | Determinism and reanalysis checks run on scratch copies only (U6, U15) |
| No version control | Snapshots and manifests (U1, U17). Offer `git init` at close-out |

---

## Documentation / Operational Notes

- Sleep must be off during long runs. This is a user action.
- Queue state is under `runs/queue/`. Nothing that must survive a restart lives in `/tmp`.
- Labels, keys, and the held-out `key.md` and `key.jsonl` never leave this machine. The labeller receives only the review markdown.
- `docs/labelling-log.md` contains no keys.

---

## Definition of Done

1. Sandbox milestone (U8) passed: the five F4 tests pass with no skip, `validate` and `selfcheck` pass, the full suite passes with every skip explained.
2. Stored-evidence and determinism checks (U6) pass, or each mismatch is listed with its cause.
3. Held-out detector report (U15) saved, with counts, intervals, the miss list, the false-alarm list, freeze hashes, and the dev-to-held-out gap.
4. Docs pass `check_docs.py` (U16). `grader/FEEDBACK.md` no longer makes the false "unmodified" claim.
5. Secrets scan has zero hits in project-owned files. Snapshot and manifest saved.
6. Nothing is running.

This does not claim the detectors catch every fabrication. It states what was measured, on what data, and what was missed.

---

## Sources & References

- **Origin:** `last_stretch_plan.md` (project root). Its acceptance criteria are kept where still valid. Its unit order, its DeepSeek stages, and its grader and G1 to G7 units are superseded by this plan.
- Related code: `internal/runner/runner.go`, `internal/config/profile.go`, `internal/evaldet/evaldet.go`, `internal/grade/grade.go`, `internal/proxy/proxy.go`, `internal/verify/verify.go`, `internal/agents/launcher.go`, `tools/queue/run_queue.sh`, `cmd/harness/run.go`.
- Project state: `HANDOFF.md`, `README.md`, `docs/not-performed.md`, `grader/FEEDBACK.md`.
- Earlier plans: `docs/plans/2026-10-03-001-feat-agent-eval-harness-plan.md`, `docs/plans/2026-10-03-002-feat-swe-lite-suite-plan.md`.
