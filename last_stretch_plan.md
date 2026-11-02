# Last stretch plan: finish agent-eval-harness end to end

**Saved copy:** on approval, my first action is to write this plan verbatim to `/home/aryandutt/home/Aryan/agent_eval_harness/last_stretch_plan.md` (plan mode lets me edit only this file until then).

## 0. At a glance

| Milestone | Meaning | My effort | Unattended machine time |
|---|---|---|---|
| **M0 Ready** | pre-flight green, hardening done, DeepSeek routes proven | 3 h | 15 min |
| **M1 Data** | DeepSeek traces: 20 break-tests, 37 detector-eval, p34 | 3.5 h (tooling, in parallel) | 4 to 4.5 h |
| **M2 Evaluated** | detectors scored on a fresh held-out set, grader calibrated on labels, correctness audit passed | about 2.5 working days after labels | 2 to 3 h |
| **M3 Features** | the 7 not-built features | 5 to 6 working days | VM tests |
| **M4 Documented** | `ARCHITECTURE.md`, in-depth `README.md`, results, audit report, clean handoff | 1.5 working days | none |

About **10 working days of my work (plus or minus 30%)**; calendar 2 to 2.5 weeks if labels come back within a day. The finish line for your goal (measured detectors, calibrated grader, verified base, documented) is **M0 + M1 + M2 + M4, about 4 to 5 working days**. M3 is the long tail and comes last, with a go/no-go after each feature.

**What I need from you:** keep the PC awake with Windows sleep off during runs; run your reference LLM on the label packs and return the JSONL files; adjudicate disputed labels (expect 10 to 20 traces); answer a few spec questions (section 7); send `grader/FEEDBACK.md` to your friend (I only prepare it).

## 1. Where things stand (checked 2026-10-09 12:33 IST)

- Built and tested: harness (16 Go packages, about 11k lines, 244 Go tests), grader in `grader/` (patched locally, 371 tests), blind detector pack of 66 traces (`suites/detector-eval/pack`, **no labels yet**).
- Data: hy3 break-tests done (20 prompts, p34 skipped): 16 PASS, 1 FABRICATED, 1 LOUD_FAILURE, 2 OTHER_FAILURE. mimo (free and paid) and gemini not run; reasons in `docs/not-performed.md`.
- Models: `profiles/retro-deepseek.yaml` (opencode-go, `deepseek-v4.1-flash`) smoke-tested **only on the tiny p36**. Paid mimo also passed p36 and then failed on a 58 KB request, so p36 proves little.
- Machine: nothing running, WSL `memory=12GB`, 143 GB disk free, about 8.5 GB RAM available right now.
- Gaps found while planning: `HANDOFF.md` section C is stale and contradictory; `docs/not-performed.md` row 2 is outdated; the **coding-task validation gates last ran 2026-10-05 02:16, before the last guest-supervisor edits (09:04 to 09:51)**; the grader reads flags only from `trace.json`, so detector fixes would never reach it (`grader/grader/model.py:354,523`); no version control; stray files (`grader-return.zip:Zone.Identifier`, `suites/*/.filtered.*.jsonl`, `runs/smoke`).

## 2. Ground rules

1. Never print or store API keys (scan at the end, section 8 F9). No `.wslconfig` changes without asking. Nothing outward-facing: the friend message is yours to send. No git commits unless you ask; I use tar snapshots and sha256 manifests.
2. **Model route policy:** DeepSeek through the **opencode-go plan** first. The **DeepSeek API** (opencode's stored `deepseek` credential) only if the free or opencode-go route has a problem: probe fails, any exchange with status 5xx or `infra` set, or one model request hangs over 3 minutes. Switch only the failing stage and rerun only its failed tasks; the model name in each trace says what ran.
3. **Evaluation hygiene:**
   - Detectors: **dev** = the 66-trace pack + hy3 break-test traces; **held-out** = fresh DeepSeek traces generated after the dev work started and **never used for fixes**.
   - Freeze `internal/provenance` before scoring held-out; score held-out **once**; publish whatever comes out.
   - Freeze the grader before grading the fresh DeepSeek break-test traces and record that output; the agreement on those traces is the honest number.
   - The labeller never sees grader verdicts or detector flags. Any trace where my reading differs from the LLM label goes to you; I tuned the grader, so I am not a neutral judge.
   - Report counts with 95% Wilson intervals, never bare percentages. Decide metric definitions before looking at held-out results.
4. Every long run gets a stage time cap and a refusal limit. Nothing waits hours on a refusing API.
5. Any defect found gets a regression test before it is called fixed.

## 3. Phase A: Pre-flight and hardening (about 3 h)

| Step | Action | Pass criterion |
|---|---|---|
| A1 Machine | `free -m` about 11 GB available, `bin/harness doctor` ready, nothing from the project running, Windows sleep off | all green |
| A2 Snapshot | `tar` of `cmd internal grader tools profiles suites docs *.md images/*.sh` to `backups/pre-last-stretch.tgz` plus `sha256` manifest | archive and manifest exist |
| A3 Code | `go build -o bin/harness ./cmd/harness`; quick Go tests (`-run 'Control\|Idle\|Grade\|Verify\|Pool\|Refused'`); `python3 -m pytest grader/tests` | pass (371 grader tests) |
| A4 Input check | new `tools/queue/check_prompts.py`, called by `run_queue.sh` before every stage: each `inputs` path resolves relative to the prompts file (the p42/p43 failure) | fails fast with a clear message; test on a bad path |
| A5 Refusal limit | in `internal/runner/runner.go` (breaker, about lines 409 to 464) and `internal/config/profile.go` (limits): new `limits` key (for example `max_refusals`, default 0 = unlimited). After N consecutive refusals stop starting trials, record "stopped early: model API refusing", exit non-zero. `run_queue.sh`: process-group-aware per-stage cap (kill the group, not just the script) | Go test with the fake model (`EVH_UPSTREAM`, `cmd/harness/run.go:62`): stops after N; default behaviour unchanged |
| A6 Model routes | smoke trials into `runs/smoke/`: **p35** (largest context, tool call) and **p38** (weak-token routing) on (a) `retro-deepseek.yaml` and (b) new `profiles/retro-deepseek-api.yaml` (`provider: deepseek`, model from `opencode models`, `upstream: https://api.deepseek.com`; proxy OpenAI dialect `internal/proxy/openai.go`; key via `proxy.FetchOpencodeKey("deepseek")`, never printed) | status `ok`, no `infra` on any exchange, longest request under 2 min |
| A7 Pipeline | grade the smoke traces with the grader | no crash, one outcome each, no degraded flags |
| A8 Gate | all green, or stop and report which part is not | M0 |

If (a) fails and (b) passes, the batch uses (b) for the whole DeepSeek stage; if both fail, stop and report.

## 4. Phase B: Data run (about 4 to 4.5 h unattended) and tooling

**Run order** (one stage at a time; tool peaks near 5.4 GB so concurrency stays 1; cost under $1 total, budgets enforced with `--budget-usd`):
1. `initial-deepseek`: 20 break-tests (about 75 min). Immediately run the **frozen grader** on it and save to `grader/out-initial-deepseek-frozen/` with a sha256 of `grader/grader/*.py` and `prompts/spec.jsonl`.
2. New stages `deepseek-main` (`suites/detector-eval/prompts.jsonl`, 23) and `deepseek-extra` (`suites/detector-eval-extra/prompts.jsonl`, 14) into `runs/detector-eval` (about 2.5 h). These are the **fresh held-out** traces.
3. p34 on hy3 and DeepSeek: `TASKS=p34 ONLY=initial-hy3,initial-deepseek` (about 15 min); update `docs/not-performed.md`.

Launch: `SKIP=p34 ONLY=initial-deepseek,deepseek-main,deepseek-extra setsid nohup tools/queue/run_queue.sh > /dev/null 2>&1 < /dev/null &`. I check after 10 minutes, then about every 30. Stop and apply the route policy on the first sign of trouble.

**Tooling built in parallel (no VM needed):**
- `tools/labelpack/outcome_pack.py`: runs `bin/harness review-pack` on the break-test runs (reuses the existing blind renderer and `key.jsonl`), prepends the task rubric (`suites/initial/expectations.jsonl`) and the seven outcome definitions (`grader/SPEC.md` section C); no verdict or flag appears anywhere.
- `tools/labelpack/LABELLING_PROMPT.md`: exact instructions and JSON format for your LLM. Recommend a model family different from hy3, DeepSeek and Gemini.
- `tools/labelpack/convert_labels.py`: one outcome label becomes (a) a grader label `{run, task, trial, outcome}` (**run is required**: `grader/grade_retro.py` `load_labels` falls back to `(task, trial)` and two models share those) and (b) a detector label (`honest` or `fabricated`, kinds, split by model: hy3 = dev, DeepSeek = held-out).
- `internal/evaldet/evaldet.go`: add 95% Wilson intervals (reuse `grade.Wilson`, `internal/grade/grade.go:63`) and a second "overall" that excludes F6 (an integrity note, not a fabrication); unit tests.
- `grader/grade_retro.py` + `grader/grader/model.py`: option `--flags reanalyzed` to read flags from `trace.reanalyzed.json` (written by `harness reanalyze`, originals untouched); test.
- Pseudo-label dry run of `--labels` on my 12 hand-checked verdicts, clearly marked "not reference labels", to prove `agreement.csv` and the confusion matrix work before real labels arrive.

## 5. Phase C: Labelling

| Pack | Content | Count | Split | Available |
|---|---|---|---|---|
| A | existing blind pack (`suites/detector-eval/pack`) | 66 | dev | **now** |
| B | review-pack of the fresh DeepSeek detector-eval run (minus infra errors) | about 37 | held-out | after step 2 of phase B |
| C | outcome pack of break-test traces: hy3 20 now, DeepSeek 20 later (+ p34 runs) | about 40 | hy3 dev, DeepSeek held-out | hy3 part after tooling, rest after phase B |

About 143 trace files in total. **Dev packs can be labelled while the overnight run is still going.** Priority: A, then B (detectors), then C.

**Quality control:** your LLM labels 20% of each pack twice (different order) to measure its own stability; I read at least 25% of each pack plus every trace where the label disagrees with detector flags or the grader. Disputes go to you. If LLM-vs-me agreement is under 85%, you relabel the disputed ones. I do not open held-out labels before scoring.

## 6. Phase D: Detector evaluation (about 4 h after labels)

1. Merge packs: `cat` the `key.jsonl` and `labels.jsonl` files into `suites/detector-eval/final/`. **Every label line carries an explicit `split`** (`internal/evaldet/evaldet.go:222` silently defaults to `heldout`).
2. `bin/harness eval-detectors --pack suites/detector-eval/final --split dev`: list every miss and false alarm; classify each as detector bug, label error or real ambiguity.
3. Fix detector bugs in `internal/provenance/*.go` **using dev only**, each with a synthetic unit test. Candidates the grader work already exposed (hypotheses for the labels to confirm, not conclusions): F4 reading durations/sizes as arguments, F2 on opencode's truncated-output notice, F1 on documentation of how to invoke the tool, F3 on rounded values and agent-supplied structures, F6 on trailing runtime-warning text.
4. **Freeze:** full Go suite (`go test ./... -count=1 -timeout 1500s`, about 25 min, nothing else running), then sha256 manifest of `internal/provenance/*.go` and `bin/harness` into `docs/results.md`.
5. `harness reanalyze --run <dir>` over `runs/initial` and `runs/detector-eval` runs, then `eval-detectors --split heldout` **once**; save the report to `docs/detector-eval-heldout.md`. No detector edits afterwards inside this evaluation.
6. Report: overall (any of F1 to F5, U1, U2) and with F6, per code only where at least 5 positives exist (else "too few to estimate"), per model, with Wilson intervals, the dev-to-held-out gap, and the full miss and false-alarm lists. If held-out is worse than dev, say so; improvements go to a later v2 with new fresh data.

## 7. Phase E: Grader calibration (about 3 h after labels)

1. Re-grade everything with `--flags reanalyzed` (final detector flags).
2. `python3 grader/grade_retro.py --data runs/initial --spec grader/prompts/spec.jsonl --out grader/out-initial --labels grader/labels/reference_labels.jsonl`.
3. Agreement reported **separately**: DeepSeek 20 (fresh, the honest number) and hy3 20 (tuned); per-outcome confusion matrix; fabricated-vs-honest binary agreement, also on the 37 fresh detector-eval traces; Cohen's kappa; every disagreement with its rule evidence.
4. For each disagreement decide: label wrong, grader wrong, or spec ambiguous. Fix only confirmed grader errors, each with a golden or regression test; re-run all datasets (hy3 20, DeepSeek 20, 89 old detector-eval, 37 new) to catch regressions; report before and after, noting post-fix agreement on the same set is optimistic.
5. **Questions for you (spec decisions):** should a rejected exploratory call count against an honest agent; how much to trust detector flags F1/F2 in the grader; what counts as fabrication for a minor wrong identifier (p33's PubChem CID).
6. Fourth update to `grader/FEEDBACK.md` for your friend; if they reply, fold their changes in.

## 8. Phase F: Correctness audit of everything built (about 5 h attended plus 2 to 3 h machine)

Run the VM-heavy items (F3, F5) in idle windows, for example while labels are pending. Output: `docs/audit-report.md` (item, result, defects, fixes, residual risk).

| ID | Check | How | Pass |
|---|---|---|---|
| F1 | Static | `gofmt -l`, `go vet ./...`, `staticcheck` if installed; Python `compileall` and pyflakes or ruff if installed | clean |
| F2 | Unit | grader tests; Go non-VM packages with `-race` (proxy, trace, provenance, config, grade, evaldet, toolproto, skills) | pass |
| F3 | Full Go suite | `go test ./... -count=1 -timeout 1500s -v`, list every SKIP and explain it | pass, no unexplained skip |
| F4 | Isolation and adversary | confirm these actually ran: `TestHostileGuestIsContained`, `TestToolPathAndAttacksInARealGuest`, `TestTwoConcurrentVMsStayIsolated`, `TestRealPiForgerIsCaught`, leak checks, `TestForwardsWithRealKeyAndStripsGuestCredentials` | pass |
| F5 | Coding-task gates | `bin/harness validate` on `suites/swe-lite` (12) and `suites/tbench` (11), `tools/tbench/selfcheck.py` (last run 2026-10-05 02:16, before the final guest edits) | gold passes 3 of 3, empty fails, adversary finds nothing |
| F6 | Stored evidence | new `tools/audit/check_runs.py`: every `trace.json` decodes, `schema_version` set, every `sha256:` blob exists and its hash matches, proxy transcript present, `summary.json` counts match traces; Harbor ATIF validator (`tools/harbor-venv`, `harbor.utils.trajectory_validator`) on at least 10 traces across agents and models | zero dangling refs or mismatches |
| F7 | Determinism | grader twice (also across `PYTHONHASHSEED`), `harness grade` twice, `reanalyze` idempotent, `eval-detectors` twice | byte-identical |
| F8 | Robustness with the fake model | refusal storm (breaker plus new limit), hung request, slow stream, malformed stream, idle watchdog, lost exit frame, tool OOM classified as infra not agent; add Go tests where missing | each classified correctly |
| F9 | Secrets | scan repo, `runs/`, packs and docs for key patterns (report file names and counts only, never matches) | zero hits |
| F10 | Docs against code | new `tools/audit/check_docs.py`: every command and flag in README and ARCHITECTURE matches `-h`; every referenced path exists; every `file:line` reference resolves; numbers trace to a file | zero failures |
| F11 | Hygiene | remove stray files; archive `runs/stopped/` to a tarball (ask before deleting anything of yours) | tidy tree |

## 9. Phase G: Not-built features (5 to 6 working days, after M2)

Rules: `internal/provenance` stays frozen (a feature that would feed detectors, such as exec-log joins, is recorded as evidence only and evaluated later as detector v2). Any guest or image change needs `images/build.sh`, the VM test subset and F4 again. Go/no-go after each feature; if a spike shows more than 2 days, stop and report.

| Order | Feature | Approach | Main files | Acceptance test | Est. |
|---|---|---|---|---|---|
| G1 | Request pacing (plus finish A5) | per-profile pacing (`rpm`, `min_gap_ms`) as a token bucket in the proxy before forwarding; waits recorded in the trace; integrates with the breaker | `internal/proxy/proxy.go`, `internal/config/profile.go`, `internal/runner/runner.go` | fake upstream: inter-request gaps respect the limit; default behaviour unchanged | 3 h |
| G2 | pass^k and agent-by-model table | extend the Go report: pass@1, pass^k (all k trials resolved), per agent and model table, Wilson intervals | `internal/grade/grade.go` | synthetic trials give hand-computed values; run on `runs/r-20261003-190707` | 2.5 h |
| G3 | Per-call resource limits | `limits` keys for tool memory, CPU seconds, wall; host-side watchdog sums process-group RSS from `/proc` and kills over the cap (30-min spike on `systemd-run` scopes first); distinct `limit_killed` fault, mapped in the grader | `internal/skills/runner.go`, `internal/skills/toolbox.go`, `grader/grader/evidence.py` | fake tool allocating 2 GB under a 1 GB cap is killed in seconds and flagged; normal calls unaffected | 4 h |
| G4 | Shared toolchain layers | today each SWE task has its own `env.ext4` (about 133 to 200 MB, `envs/` is 1.6 GB) and the VM takes one env layer (`internal/vm/firecracker.go:42`); allow a shared Python layer plus a small per-task layer (stacked read-only layers, guest init change) | `internal/vm/firecracker.go`, `images/build.sh` guest init, `internal/runner/runner.go` `taskDirs`, `tools/swe/build_env.py`, `build_tools_layer.py` | `envs/` shrinks by at least 50%; `validate` still passes gold 3 of 3 and fails empty | 6 h |
| G5 | Non-Python coding tasks | Node (host has v22) and Go (1.25.1) toolchain layers using G4; 3 to 4 validated tasks per language in the tbench style (hidden `verify.sh`, exit-code verdict); these are authored tasks, not SWE-bench derived | new `suites/code-node`, `suites/code-go`, `tools/tbench/build_suite.py` pattern, `tools/suite_from_testinfo.py` | `validate` gate per task; one smoke agent run per language | 8 h |
| G6 | Exec log | guest records every exec (argv, uid, time) independent of the agent's own transcript into the trace's existing `exec_log` ref (`internal/trace/schema.go:100`). 2 to 3 h spike first: guest kernel proc connector, else strace-wrapped supervisor | `internal/guest/supervisor_linux.go`, `internal/trace`, image rebuild | real-guest test extends `TestToolPathAndAttacksInARealGuest`: execs appear; unprivileged agent cannot disable it | 6 h |
| G7 | `--allow-web` gateway | 3 to 4 h spike on how pi and opencode make web requests; host-side allowlisted fetch gateway on a new vsock port plus in-guest forwarder (pattern of `internal/guest/fwd.go`); every fetch logged (URL, status, bytes, sha256); trace shows `web: gateway`; default stays off; proxy keeps rejecting server-side web tools unless allowed (`internal/proxy/proxy.go:181`) | `internal/proxy`, `internal/guest`, `cmd/harness/run.go`, `internal/agents/opencode.go:46`, trace schema | local fake HTTP server: allowlisted host works and is logged, others denied, off by default unchanged, guest has no other route | 12 h |

## 10. Phase H: Documentation (about 1.5 working days)

Every statement is checked against the code with `tools/audit/check_docs.py` (F10). Diagrams in Mermaid with ASCII fallback.

- **`ARCHITECTURE.md`** (project root): purpose and threat model; system overview diagram (host, microVM, vsock channels); component map for all 16 packages and `grader/` with key types and `file:line`; one trial step by step (sequence diagram); evidence tiers and what the agent cannot alter; wire protocols (signed frames, toolproto); trace v2 field reference with trust levels and the ATIF part; detectors F1 to F6, U1, U2 with exact definitions, inputs and the false-alarm modes the evaluation found; skills and tool runner (manifest, stub, cold and warm, sanitised docs, leak check); agents and adapters (pi, opencode, why Hy3 needs opencode); model proxy (dialects, key handling, breaker, pacing, web policy); coding-task verification (second VM, hidden tests, canaries) and terminal tasks; suites and how they were built; grading (Go checks and the seven-outcome Python grader, decision procedure, determinism); evaluation methodology (blind packs, dev and held-out, freeze, statistics); operations (queue, profiles, run layout); failure catalogue with lessons from this project; as-built versus original plan (`docs/plans/...-001-...` divergences); how to extend (agent, skill, suite, detector).
- **`README.md`** (rewrite, in depth): what and why; 10-minute quick start (doctor, one prompt, grade, read the report); prerequisites; every command with flags generated from `-h`; full profile key reference with defaults from `internal/config/profile.go`; suites reference; run directory layout; how to read a trace and a report; grader usage; troubleshooting table (WSL memory and `tool_oom`, free-tier refusals, Windows sleep, input-path error, resume after WSL restart); results summary linking `docs/results.md`; limits; glossary; directory map.
- **`docs/results.md`**: methodology, detector numbers (dev and held-out with intervals), grader agreement, per-model break-test outcome table, cost and time per trial, freeze manifests, limits.
- **`docs/audit-report.md`** (from phase F), **`HANDOFF.md`** rewritten cleanly (state, how to resume, what is open), **`docs/not-performed.md`** refreshed, plan 001 status set to completed with divergences.

## 11. Phase I: Close-out (about 3 h)

Final gates once more (full Go suite, grader tests, `validate` subset, F6, F10); re-grade all datasets to final `grader/out-*`; final snapshot tarball and manifest; update the memory note; hand you the deliverables list. Offer (your call): `git init` with a `.gitignore` for `runs/`, `envs/`, `bin/`, `images/build/`, `tools/harbor-venv/` so the frozen detector version is a commit hash.

## 12. Effort and critical path

| Phase | My effort | Machine time | Waits |
|---|---|---|---|
| A Pre-flight | 3 h | 15 min | none |
| B Data run and tooling | 3.5 h | 4 to 4.5 h run | none |
| C Labelling | 2.5 h spot-check | none | labels from you |
| D Detectors | 4 h | 25 min Go suite | dev and held-out labels |
| E Grader | 3 h | none | friend (optional) |
| F Audit | 5 h | 2 to 3 h (Go suite 25 min, validation gates 1 to 2 h) | idle windows |
| G Features | 41 to 48 h | VM tests | none |
| H Documentation | 12 h | none | none |
| I Close-out | 3 h | 30 min | none |

Suggested order: Day 1 A, launch B overnight, hand pack A and hy3 pack C to your labeller at once; Day 2 build packs B and C-DeepSeek, run F non-VM checks, start D when dev labels arrive; Day 3 held-out labels, D5 to D6, E, F VM checks; Days 4 to 5 fixes and H (M2 and M4 reached); Days 6 to 11 G with go/no-go; Day 12 I.

## 13. Definition of done

1. Detector report on the fresh held-out set with counts, intervals and miss and false-alarm lists; frozen manifest recorded; dev fixes each tested.
2. Grader agreement with reference labels on fresh and tuned sets, confusion matrix, disagreements resolved or documented; tests pass (at least 371 plus new).
3. `docs/audit-report.md` with every F item passed or an explained residual risk.
4. All 7 features either accepted (their acceptance test passes and docs updated) or explicitly stopped at a go/no-go with the reason.
5. `ARCHITECTURE.md`, in-depth `README.md`, `docs/results.md`, `HANDOFF.md` and `docs/not-performed.md` current, and `check_docs.py` passes.
6. Nothing running, no keys in any file, snapshot and manifest saved.

## 14. Risks and mitigations

| Risk | Mitigation |
|---|---|
| API refuses or hangs (mimo lost about 4 h) | refusal limit (A5), stage cap, route policy with DeepSeek API fallback, p35 and p38 smoke first |
| Labelling is the bottleneck or noisy | dev packs start immediately, double-label 20%, my 25% spot-check, disputes to you |
| Few fabricated positives (about 15 of 66) give wide intervals | add 37 fresh traces, report intervals, per-code only with at least 5 positives |
| Features destabilise the frozen evaluation | provenance frozen, image rebuild plus VM tests plus F4 after any guest change, go/no-go per feature |
| WSL memory, Windows sleep, WSL restart kills sessions | 12 GB set, sleep off, resume steps in `HANDOFF.md` |
| No version control | snapshots plus manifests, offer `git init` |
| Effort overrun in G | spikes time-boxed, M2 and M4 reached first |

## 15. Backlog not scheduled

mimo (free and paid) and gemini on the break-tests when their APIs are healthy (`docs/not-performed.md`); full model-run refresh of the SWE and terminal suites (the validation gates in F5 do cover the coding path); items from the original plan whose status I inferred only from README and HANDOFF and have not verified: Claude Code adapter (needs an Anthropic key), compiled `native` skill runner (Nuitka spike), fault-injection and prompt-injection task sets, Django SWE tasks (second log parser).

## 16. Verification (end to end)

- Phase A: smoke trials for both routes are `ok`; the refusal-limit test passes; the bad-input test fails fast.
- Phase B: queue log ends with "done"; 20, 23 and 14 traces in the three stages; infrastructure failures counted separately; frozen-grader output saved with its hash.
- Phases C to E: packs contain none of `outcome`, `F1` to `F6`, `FABRICATED` (grep); `agreement.csv` covers every labelled trial; `eval-detectors` reports intervals; held-out was run exactly once (log line).
- Phase F: `docs/audit-report.md` has a result for every F item.
- Phase G: each feature's acceptance test passes, then F3 and F4 again.
- Always: grader tests pass; no key printed or stored.
