# Handoff: where the project stands and what to do next

Written 2026-10-07 so a new session can continue from files alone. Read `README.md` for how the harness works; this file is the state and the checklist.

## State

- Everything in `README.md` is built and the **full test suite passes** (`go test ./... -count=1 -timeout 1500s`; about 25 minutes because some tests boot real microVMs). The guest image (`images/build/rootfs.ext4`) and `bin/harness` are current; rebuild with `images/build.sh` and `go build -o bin/harness ./cmd/harness` after changing guest code.
- **Nothing is running.** The long batch of runs was paused on purpose (see "Run the data" below).
- Fixed and tested recently: lost exit frames (shutdown race, 15-minute silence limit, oversized lines), a stall watchdog (`limits.idle_s`, default 900 s), a cooldown when a model API refuses requests, tasks where "do nothing and say so" is the correct answer (`do_nothing_ok`).
- The friend's grader (`grader-return.zip`) is unpacked in `grader/` and checked (section B, done 2026-10-07). It is **not calibrated**: see `grader/FEEDBACK.md`.
- **Waiting on the user:**
  1. Decide what to do with `grader/FEEDBACK.md`: send it to the friend, and/or apply its one-line fix to `grader/grader/decide.py:106` (review queue 52% to 2%, no outcome changes). `grader/` was patched locally on 2026-10-07 (degraded fix plus rule fixes 1 to 3, see top of `FEEDBACK.md`); all fixes from `FEEDBACK.md` section 3 are now applied (see its second update); the review pack is built in `suites/detector-eval/pack` and waits for your labels (section E).
  2. Go-ahead for section C (the 21 break-test prompts on three models, 5 to 6 hours).
  3. Labels on the blind review pack, to measure the fabrication detectors (needs the user's separate LLM plus a spot-check).

## Checklist

### A. Orient (about 10 minutes)
- [ ] Read this file, `README.md`, and the memory notes.
- [ ] `go build -o bin/harness ./cmd/harness && bin/harness doctor` (needs `/dev/kvm`; the process may need to run Firecracker through `sg kvm`, the harness does that itself).
- [ ] Optional quick check: `go test ./internal/... -count=1 -timeout 600s -run 'Control|Idle|Grade|Verify|Pool|Refused'`.

### B. Take in the grader (done 2026-10-07)
- [x] Unpacked into `grader/`; README, NOTES and SPEC read. Its own tests: 353 pass, golden fixtures regenerate byte-identically.
- [x] Ran on `runs/detector-eval`: 89 distinct trials (151 trial dirs; 25 retried, last attempt graded), output in `grader/out-detector-eval/`.
- [x] Against its spec: all output files, only the seven outcomes (LOUD_FAILURE never occurs on this data), every trial graded, all CSV cells filled, byte-identical across runs, hash seeds and working directories. **Fails** the review-queue criterion (46 of 89): zero-call trials are marked "degraded" (`decide.py:106`).
- [x] Compared with `bin/harness grade` (run on a scratch copy; the stored runs were not touched) and with the detector flags. Every F3-only disagreement and each fabrication verdict I read is in `grader/FEEDBACK.md`. Headline: 7 of the 21 fabrication verdicts look wrong (4 of 10 completed controls graded FABRICATED, 3 of them wrongly), 2 fabrications are graded honest refusals (t06, t18 on hy3).
- [x] Integration: standalone, command documented in `README.md`. No wrapper needed (`--data runs/<dir>` works directly).
- [ ] Still open from B: the grader's rules for the 21 break-test prompts (p33 to p55) have never run on a real trace; that happens in section C.

### C. Run the data the grader needs
- 2026-10-08: only hy3 was run for the break-tests (20 prompts, p34 skipped); mimo (free and paid) and gemini are in `docs/not-performed.md`. Graded with the patched grader into `grader/out-initial/`; the 12 non-PASS trials were hand-checked (see the third update in `grader/FEEDBACK.md`). Next: reference labels on these 20 traces (D).
- 2026-10-07 19:25: models for the break-test batch are now **hy3, mimo-v2.6-flash (paid, opencode-go subscription; `profiles/retro-mimo-go.yaml`) and gemini-3.5-flash-lite (`retro-suite.yaml`, pi)** (a deepseek-v4.1-flash profile, `retro-deepseek.yaml`, exists too but is not used); mimo and big-pickle are left out because the free APIs refuse requests (mimo ended p33 as `upstream` after 2 h of cooldowns). hy3 is complete (18 + p42/p43 rerun in a second run dir; p34 skipped). `runs/queue/resume4.sh` runs the other two: mimo with the free model first (one-trial probe, 2 h cap) and the paid `mimo-v2.6-flash` only for tasks the free one could not finish (model name stays in each trace), then gemini. Gemini free quota is 500 requests/day: leave it for last.
- 2026-10-07 16:06: `tools/queue/launch_when_idle.sh` is waiting for the chess job (`bulwark`) to finish, then starts the batch (max wait 2 h, then starts anyway). Check `runs/queue/queue.log`.
- 2026-10-07: batch stopped on purpose (user: finish the rest of the project first, run it overnight). Nothing is running; WSL has 12 GB. Launch tonight with `SKIP=p34 ONLY=initial-hy3,initial-mimo,initial-pickle setsid nohup tools/queue/run_queue.sh > /dev/null 2>&1 < /dev/null &` (about 5 to 6 hours; partial runs are in `runs/stopped/`).
- Skipped for now: p34 (host OOM). Tests left out for non-skill reasons are listed in `docs/not-performed.md`; keep that list current. Launch with `SKIP=p34 ONLY=initial-hy3,initial-mimo,initial-pickle tools/queue/run_queue.sh`.
- 2026-10-07: first attempt stopped (host OOM killed p34; partial runs in `runs/stopped/`). `.wslconfig` now sets `memory=12GB` (created at the user's request). After the WSL restart, check `free -m` shows ~11 GB, then relaunch the command below.
- [ ] `ONLY=initial-hy3,initial-mimo,initial-pickle tools/queue/run_queue.sh` runs the 21 break-test prompts on three models into `runs/initial/` (about 5 to 6 hours; log in `runs/queue/queue.log`). Remove the partial directory `runs/initial/r-20261005-035244` first (left by the stopped queue; it has no summary of finished trials).
- [ ] Grade those runs with the grader. Open the evidence card of every non-PASS trial and check it against the trace by hand for the first batch.

### D. Calibrate the grader
- [ ] Give the first graded batch to the user's separate LLM (or the user), collect its outcomes as reference labels, and compute agreement per outcome. Send disagreements to the friend (rule evidence is in `rubric_results.jsonl`), tighten rules, re-run.

### E. Measure the fabrication detectors (main open item for the user's top priority)
- [ ] `bin/harness review-pack --out suites/detector-eval/pack runs/detector-eval/r-*` (infrastructure failures are left out; `key.jsonl` stays on this machine).
- [ ] Give the user `pack/*.md` and `pack/README.md` to label (honest or fabricated, with kinds); labels go to `pack/labels.jsonl` (`split` = `dev` or `heldout`).
- [ ] `bin/harness eval-detectors --pack suites/detector-eval/pack --split dev`; fix detector bugs found on `dev` only; then freeze and score `--split heldout` once.

### F. Optional, as time allows
- [ ] The rest of the batch: `tools/queue/run_queue.sh` (SWE on mimo and big-pickle, more detector-evaluation traces; about 15 hours). Use `ONLY=...` to pick stages.
- [ ] The Gemini remainder (`suites/detector-eval/prompts.gemini-rest.jsonl`) only when its quota is back (`tools/queue/gemini_gate.sh` probes for that; the stage is the last one in the queue).
- [ ] Not built: the `--allow-web` option, per-call resource limits and exec log, shared toolchain layers, request pacing, pass^k and an agent-by-model table in the report, non-Python coding tasks.

## Useful facts

| Thing | Detail |
|---|---|
| Models | Gemini (`profiles/retro-eval.yaml`, via pi), Hy3 (`retro-hy3`, via opencode), `opencode/mimo-v2.6-flash-free` and `opencode/big-pickle` (`retro-mimo`, `retro-pickle`, free). Profiles for SWE: `swe-lite`, `swe-hy3`, `swe-mimo`, `swe-pickle`; terminal tasks: `tbench-*` |
| Keys | never print them. Gemini's lives in pi; Hy3's in opencode's own credential store; the host reads them and the guest only sees a dummy |
| Gemini quota | 500 requests per day per model; our own runs used it up on 2026-10-04 and it had not come back by 2026-10-05; test with `tools/queue/gemini_gate.sh` |
| Free models | `mimo-v2.6-flash-free` returns `FreeUsageLimitError` after heavy use; the runner now pauses new trials when the API refuses |
| Memory | the retrosynthesis tool peaks near 5.4 GB on a 7.6 GB host; trials are killed sometimes and retried; guest VM is 512 MB for retrosynthesis profiles. Raising WSL's `memory=` would help (the user's choice) |
| `/tmp` | not durable: a restart clears it. Everything needed is under the project folder |
| Suites | `suites/initial` (21 retrosynthesis break-tests), `suites/swe-lite` (12 tasks), `suites/tbench` (11 tasks), `suites/detector-eval` and `detector-eval-extra` (out-of-scope and control prompts) |
| Stored runs | `runs/detector-eval/*` (Gemini 6 usable, Hy3 about 3 passes, mimo 23 prompts; the Gemini `r-20261005-*` directories are all quota failures), `runs/r-20261004-2046*`/`2056*` (terminal tasks), `runs/r-20261003-190707` (SWE on Gemini, 5 of 11 resolved), `runs/r-20261005-001323` (partial SWE on mimo, from before the fixes) |
| For the friend | `grader-handoff.zip` in the project root (standalone brief, prompts, rubric, skill reference); the friend's return is `grader-return.zip` (unpacked in `grader/`); `grader/FEEDBACK.md` is what to send back |

## Starting prompt for a new chat

> We are continuing the agent-eval-harness project in this folder. Read `HANDOFF.md` and `README.md` first and follow the checklist, starting with section B. My friend's grader is the zip at `<path to the zip>`; unpack it into `grader/`, run its tests, and check it against its own spec on the existing traces in `runs/detector-eval`. Do not start any long runs until I say so. Never print API keys.
