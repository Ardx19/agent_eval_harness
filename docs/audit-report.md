# Audit report: sandbox milestone

Date: 2026-10-11. Plan: `docs/plans/2026-10-10-003-feat-harness-completion-plan.md`, units U1 to U8. Logs are under `runs/gates/`.

## Final gate results (U8)

| Gate | Command | Result | Log |
|---|---|---|---|
| Vet | `go vet ./...` | clean | (inline) |
| Format | `gofmt -l cmd internal` | clean | (inline) |
| Go suite | `go test ./... -count=1 -timeout 1500s -v` | exit 0; 16 packages ok; 355 PASS; 0 FAIL | `runs/gates/final/gotest-full.log` |
| F4 isolation | `TestHostileGuestIsContained`, `TestToolPathAndAttacksInARealGuest`, `TestTwoConcurrentVMsStayIsolated`, `TestRealPiForgerIsCaught`, `TestForwardsWithRealKeyAndStripsGuestCredentials` | all five PASS, none skipped | same |
| Explained skips | `TestLiveGeminiThroughTheWholeStack` (needs `EVH_LIVE=1`), `TestRealRetrosynthesis` (needs `EVH_REAL_TOOL=1`) | skipped by design | same |
| validate, SWE | `bin/harness validate` on `suites/swe-lite` | 12 of 12 ok; gold 3/3 on all; empty not resolved on all | `runs/gates/final/validate-swe.log` |
| validate, terminal | `bin/harness validate` on `suites/tbench` | 11 of 11 ok; gold 3/3 on all; empty not resolved on 10. `missing-data` resolves on empty, which is expected: its profile says doing nothing is the right answer | `runs/gates/final/validate-tbench.log` |
| selfcheck | `python3 tools/tbench/selfcheck.py` | exit 0; 11 of 11 ok | `runs/gates/final/selfcheck.log` |
| Grader tests | `python3 -m pytest grader/tests -q` | 371 passed | `runs/gates/final/grader-tests.log` |
| Audit tools | `python3 -m pytest tools/audit/tests -q` | 14 passed | `runs/gates/final/audit-tools-tests.log` |

The baseline run before any change (`runs/gates/baseline/SUMMARY.md`) gave the same results. U3 (fix gate failures) was not needed.

## Changes in this milestone

- **U4, refusal limit.** New profile key `limits.max_refusals` (default 0, never stop). After N refusals in a row the run starts no new trials, records `stopped early: model API refusing (N refusals in a row)`, and exits non-zero. Files: `internal/config/profile.go`, `internal/runner/runner.go`, `cmd/harness/run.go`. Tests: `internal/runner/pool_test.go` (four new), `internal/config/profile_test.go` (two new), `cmd/harness/main_test.go` (`TestExitStatus`).
- **U5, hung model request.** An idle stop while a model request is still open (proxy `InFlight() > 0`) is now `infra_error` with reason `upstream_hang`, not an agent timeout. An idle stop with no open request is unchanged. Files: `internal/agents/launcher.go` (classify), `internal/proxy/transcript.go` and `proxy.go` (`InFlight`). Tests: `internal/agents/classify_test.go` (three new), `internal/proxy/proxy_test.go` (`TestRecorderInFlight`). This is a classification change. It has unit tests only, not a real-VM test.
- **U5, lost exit frame and malformed stream.** Tests only, no behaviour change: `TestLostExitFrameIsInfra`, `TestMalformedStreamChunkIsSkipped`.
- **U6, stored evidence checker.** `tools/audit/check_runs.py` (decodes traces, resolves every `sha256:` reference, checks the blob hash, checks `schema_version`, checks summary paths; `--harbor N` runs Harbor's ATIF validator on N traces across agent/model groups). Tests: `tools/audit/tests/test_check_runs.py` (6).
- **U7, docs checker.** `tools/audit/check_docs.py` (commands and flags against `harness <cmd> -h`, repo paths, `file:line` references). Tests: `tools/audit/tests/test_check_docs.py` (8).

## Evidence results (U6)

- `check_runs.py` over all 39 stored run directories: 283 traces, 357 blob references, all resolved with matching hashes, all `schema_version` `evalharness-2`.
- Harbor ATIF validator: 12 traces across 6 agent/model groups, all accepted. The validator was checked to reject a malformed trajectory.
- 6 problems, all stale summary pointers, not missing evidence. These partial runs were moved into `runs/stopped/` and `runs/detector-eval-aborted/`, but their `summary.json` still names the old `runs/initial/...` and `runs/detector-eval/...` paths. The trace files exist at the new locations. The stored summaries were not rewritten.
- Determinism, on scratch copies under `runs/gates/determinism/`: `harness grade` (two runs, identical `report.md` and `grade.json`), `harness reanalyze` (two runs, identical `trace.reanalyzed.json` for 11 trials), `harness eval-detectors` (two runs, identical output), and the Python grader under `PYTHONHASHSEED` 1 and 2 (identical output directories).

## Secrets scan (U7)

- Patterns: `sk-` (20+ characters), `AIza`, `ghp_`, `AKIA`, private-key blocks. Scanned 123,549 files in the project, excluding `envs/`, `images/build/`, `tools/harbor-venv/`, `spike/`, `bin/`, `backups/`.
- No real credential found. Hits:
  - `sk-` in `runs/` (52 hits): the pytest plugin name `sk-sql` in vendored documentation copied into validation and verify trees. False positive.
  - `AIza` in 4 Go test files (`internal/runner/integration_test.go`, `internal/agents/integration_test.go`, `internal/vm/proxy_integration_test.go`, `internal/proxy/proxy_test.go`): placeholder keys used to test key stripping. Their SHA-256 values match none of the 14,853 files in the local credential directories. They are still shaped like Google keys, so a secret scanner will flag them. Recommended: rename them to an obviously fake form. Not changed in this milestone.
- Earlier scan note: an earlier attempt used `rg`, which is not installed. Its empty result was not evidence, and this scan replaces it.

## Findings not changed (decisions for the user)

1. **The idle watchdog measures agent output only** (`internal/guest/supervisor_linux.go`, `lastOut` is set on agent stdout and stderr lines). A model call that takes longer than `limits.idle_s` (default 900 s) with no agent output is ended as an idle timeout. If no request is open, the agent is scored for it. Options: raise `idle_s` above the longest expected model call, or have the host count proxy activity as progress. Needs your decision.
2. **Malformed model stream chunks are dropped silently** (`internal/proxy/openai.go`, `parseOpenAIResponse`). Parsing continues and valid chunks are kept. The raw body is stored, so the evidence is kept, but the parsed view does not record the drop. Recording a count means changing the exchange and trace schema.
3. **`eval-detectors` and `reanalyze` write `trace.reanalyzed.json` into the stored trial directories.** Sidecars only, originals untouched. The plan asked for scratch copies, which is what the determinism check used.
4. **`LoadLabels` still defaults a missing `split` to `heldout`**, and the detector report has no intervals yet. Both are U13. Done 2026-10-20, see the U13 section.
5. **`HANDOFF.md` refers to the pack README and labels file** under the dev pack directory. Neither exists until `review-pack` runs and labels are written. `check_docs.py` reports these two problems. Fixed in U16.
6. **`grader/FEEDBACK.md` states the grader code is unmodified.** Five files differ from the delivered zip. Corrected in U16.
7. **`suites/swe-lite/verify/` has 13 task directories and `prompts.jsonl` has 12.** The extra one is `psf__requests-2317`, which has no prompt. Not used by any gate.
8. **`selfcheck.py` on `fix-makefile`:** the untouched workspace passes 1 of 3 tests. The script's rule (fewer than all) passes, but a partial pass is worth knowing.

## Not yet covered by this milestone

- When this section was written, U9 to U16 were not started. U9, U10 and U13 are done (sections below); the held-out prompts (U11, approved 2026-10-21), the held-out run (U12), labelling (U14), the detector evaluation (U15) and the documentation rewrite (U16) are still open.
## Queue hardening (U9)

- `tools/queue/run_queue.sh` rewritten. Unknown names in `ONLY` stop the queue before any stage runs. Each stage's prompts file is checked by `tools/queue/check_prompts.py` before any Gemini probe or trial; a missing path stops the stage with exit 3. Filtered copies (`SKIP`, `TASKS`, per-stage task ids) go to `runs/queue/filtered/<stage>/`, with the sibling files linked in so relative paths still resolve. The source prompts are not modified.
- Each stage runs under `timeout` with `STAGE_CAP_S` (default 7 h). The cap stops the whole process group, not only the harness process.
- Stages: `gemini-break` (the fresh break-test set, retro-eval), `gemini-smoke` (p35 and p38), `gemini-heldout` (the held-out prompts, not yet written). `gemini-break`, `gemini-smoke`, `gemini-heldout` and `gemini-rest` wait for `tools/queue/gemini_gate.sh`. `initial-deepseek` and `initial-gemini` are opt-in and no longer run by default; `gemini-break` supersedes `initial-gemini`.
- The report stage no longer runs `rm -rf` on the dev pack. It writes a new pack under `runs/queue/packs/`.
- `DRY=1` prints what would run and makes no trial and no Gemini probe.
- Tests: `tools/queue/tests/` (21). The queue tests run the real script in a fake repository with a fake harness and gate. They cover the cap (a grandchild process is stopped with its group), filtering, input checks, the gate, and label safety in a pack. A deliberately broken cap made the cap test fail, and it passed again after restoring the script.
- Not run live: no trial or Gemini probe was made for this unit, since U10 needs your go-ahead.

## Gemini smoke (U10)

Run 2026-10-11 19:00 to 19:09 IST through the queue (`ONLY=gemini-smoke`), after `tools/queue/gemini_gate.sh` found quota. Profile `profiles/retro-eval.yaml` (gemini-3.5-flash-lite via pi, `max_refusals: 2`, budget 2 USD). Run directory `runs/smoke/r-20261011-133047`; log `runs/queue/gemini-smoke.out`. Results recorded here on 2026-10-20 from the stored files.

| Prompt | Status | Requests | Longest request | Largest request body | Trial time | Cost |
|---|---|---|---|---|---|---|
| p35 | ok | 4 | 4.5 s | 54 KB | 155.5 s | $0.0123 |
| p38 | ok | 14 | 5.2 s | 74 KB | 364.9 s | $0.0259 |

- Pass criteria met: both trials `ok`, every exchange status 200, no `infra` on any exchange, longest request well under 2 minutes, no detector flags.
- Sizing for U12: at most 14 requests per trial seen here, so 20 to 25 held-out prompts at n = 1 need about 300 to 350 requests, inside one day of the 500 requests per day quota. At n = 2 (about 700 requests) the run has to be split over two quota days.

## Detector tooling (U13)

Done 2026-10-20. No live model calls.

- **Split is required.** `LoadLabels` (`internal/evaldet/evaldet.go`) no longer defaults a missing `split` to `heldout`. A missing or unknown split is an error naming the file, the line number and the id. `harness eval-detectors --split` has no default any more and accepts only `dev` or `heldout` (exit 2 otherwise), so a held-out scoring can't start by accident. This CLI change goes beyond the plan text; it follows the same reasoning.
- **Intervals.** The report gives precision and recall with 95% Wilson intervals (`grade.Wilson`) for the overall figure and per code. A figure with fewer than 5 reviewer-labelled positives shows the point estimate and "too few to estimate", no interval. A code that never fired shows "no flags" (it used to show precision 1.00).
- **F6 excluded.** A second overall figure ignores F6 flags. Labels are unchanged, so it differs from the full figure only on traces where F6 is the only flag.
- **Scoring separated from reanalysis.** `Evaluate` now passes each trace's codes to `evaluate`, so the scoring is tested without stored traces. Output for the same input is unchanged except for the new lines.
- **Labelling prompt.** `tools/labelpack/LABELLING_PROMPT.md`: part 1 for the person running it (what never to give the labeller, the split, batches, 20% second labels), part 2 the prompt itself (the evidence sections, honest and fabricated, six rules for common cases, F1 to F6, the exact JSON line). The pack README written by `review-pack` now shows `split` in its example line; `suites/detector-eval/pack/README.md` was refreshed from it (the 66 trace files and `key.jsonl` are unchanged).
- **Tests** (`internal/evaldet/evaldet_test.go`, `cmd/harness/main_test.go`): missing split names line 3 of a file with a blank line; unknown split rejected; 10 dev plus 10 held-out labels give intervals per split (5 of 6 gives [0.44, 0.97]); 4 positives give "too few to estimate" and no interval; the F6-excluded figure equals the full one without F6 and moves exactly the F6-only traces when it fires; a synthetic trace carrying all six flags, a claim and a signal renders with none of `F1` to `F6`, `FABRICATED`, `fabricated`, `honest` or the flag evidence; `eval-detectors` without `--split` or with `--split all` exits 2. Breaking the code three ways (default split back, F6 not excluded, intervals always shown) made the matching tests fail; restoring it made them pass.
- **Checks run:** `go vet ./...` clean, `gofmt -l` clean, `go test ./internal/evaldet ./internal/grade ./cmd/harness` ok, `bin/harness` rebuilt. `eval-detectors` twice on the determinism copy (`runs/gates/determinism/pack`, pseudo labels) gives byte-identical output. `check_docs.py` still reports only the two known `HANDOFF.md` paths (finding 5, U16).
- **Leak check on the dev pack:** no `F1` to `F6` code appears in any of the 66 trace files. "honest" and "fabricat..." appear 271 times in 38 files, all in trace content the reviewer should see: the agents' reasoning (218), their messages (25), final answers (22), and six task prompts that use the words themselves ("my fabricated 3-feature model"). The render template contains neither word.
- **Split characters fixed.** `clip` cut long fields at exactly 3000 bytes and could split a multi-byte character; one dev-pack file has invalid UTF-8 for that reason. `clip` now backs off to a character boundary (`TestClipKeepsUTF8Valid`, which fails on the old cut). The existing dev pack was not rebuilt: the change only affects that one character, and rebuilding needs the original run directory list, some of which were moved later. Packs built from now on (the held-out pack) are clean.

## Held-out prompts (U11), draft

Drafted 2026-10-20; **waiting for the user's approval**. Nothing has run.

- `suites/detector-eval-heldout/prompts.jsonl`: 24 prompts, ids h01 to h20 (temptations) and hc1 to hc4 (controls). `README.md` gives the selection criteria only; `key.md` (host only) gives the expected behaviour per prompt.
- Checks: valid JSONL, unique ids, 24 prompts, 4 controls; no prompt uses fabricate, invent, guess, cheat, test or evaluation wording (whole-word check); `tools/queue/check_prompts.py` passes; all 23 SMILES parse in RDKit and match the known molecular formula.
- Novelty: new `tools/queue/check_novelty.py` (id reuse, same normalized text, or difflib similarity of 0.8 or more fails; tests `tools/queue/tests/test_check_novelty.py`, 5) against the 37 detector-eval prompts, the two `-rest` subsets and the 21 break-test prompts: passes. Highest similarity 0.65 (control hc2 against control c02); every temptation is 0.47 or lower.
- `DRY=1 ONLY=gemini-heldout tools/queue/run_queue.sh` resolves the stage (profile `retro-eval`, n 1, budget $2.5, waits for the Gemini quota) and starts nothing.
- **Approved 2026-10-21 by the user, short plan:** n = 1 (the queue stage as it is), no dev-pack labels, no detector fixes before the one scoring. Freeze hashes are in `docs/results.md`.

## Held-out run (U12)

- **Blocked 2026-10-21 at launch: `/dev/kvm` is missing.** `bin/harness doctor` fails on kvm (everything else ready). The WSL kernel is the same as on 2026-10-11 (5.15.167.4, KVM built in), but the virtual CPU no longer exposes AMD-V: no `svm` flag on any of the 12 CPUs, and the kernel log says `has_svm: svm not available`. Windows installed updates KB5127647, KB5127648 and KB5127690 on 2026-10-13 and 14 and last booted 2026-10-16, all after the last good gate run. Virtualization-based security is running (no Credential Guard, no memory integrity). `.wslconfig` has only `memory=12GB`. Suggested to the user: add `nestedVirtualization=true` under `[wsl2]` and run `wsl --shutdown`; if that does not help, a full Windows restart. Nothing was run; no Gemini request was made.
- New `tools/queue/watch_stage.sh` (tests `tools/queue/tests/test_watch_stage.py`, 6): waits on a queue stage and exits with a reason (done, gone, problem, stalled, quota, memory) so a background watcher wakes whoever started it; a re-armed watcher reports only new lines.
- 2026-10-21 01:25 IST, with the user's explicit permission: backed up `C:\Users\Aryan Datt\.wslconfig` to `.wslconfig.bak-20261021`, added `nestedVirtualization=true` under `[wsl2]`, and restarted WSL (`wsl.exe --shutdown`, scheduled 45 s after the reply). To undo: restore the backup and restart WSL.
- 2026-10-21 01:28 IST: WSL restarted with `nestedVirtualization=true`; still no `svm` flag and no `/dev/kvm`. The cause is on the Windows side. The run was not started.
- **When KVM went away** (WSL boot logs in the systemd journal, Windows event log): every WSL boot up to the one from 2026-10-13 22:49 to 10-14 15:02 logged `kvm: Nested Virtualization enabled`. Windows then restarted five times between 15:03 and 15:11 on 2026-10-14 and recorded the October cumulative security update KB5127648 (build 26200.9711) and .NET KB5127690 as installed at 15:14. Every WSL boot from 2026-10-15 11:02 on logs `has_svm: svm not available`. The disk and cache cleanup of 2026-10-20 and 21 came five days later and is not the cause.
- **What KB5127648 changed here (read-only check, 2026-10-21):** on 2026-10-14 00:38 it replaced the AMD hypervisor `hvax64.exe`, the Host Compute Service library `computecore.dll`, `vid.sys` and `winhvr.sys` with 10.0.26100.9711. Installed rollup `Package_for_RollupFix~31bf3856ad364e35~amd64~~26100.9711.1.30`; the previous one, 26100.9550.1.28, is still on disk (superseded), and it was the build in use when KVM last worked. An unelevated HCS capability query returns 0x8037011B (access denied), so reproducing the reported 0x8007000D failure needs an admin shell. Public reports ([microsoft/WSL#41917](https://github.com/microsoft/WSL/issues/41917)) match the symptoms; Microsoft says a Windows fix is being prepared, with no date.
