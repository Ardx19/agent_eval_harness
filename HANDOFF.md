# Handoff: where the project stands

Rewritten 2026-11-25. `README.md` explains how the harness works; this file is the state and what is still open. The full record of the last milestone is in `docs/audit-report.md` and `docs/results.md`.

## State

- **Built and tested.** The full Go suite passes (`go test ./... -count=1 -timeout 1500s`, about 25 minutes; last run 2026-11-25: 16 packages, 374 PASS, 0 FAIL, 2 skips by design, `runs/gates/deepseekapi/gotest-full.log`). Grader, audit-tool and queue-tool tests pass (`python3 -m pytest grader/tests tools/audit/tests tools/queue/tests -q`).
- **Frozen for the held-out scoring.** The detector code (`internal/provenance/*.go`, `internal/evaldet/evaldet.go`) has not changed since the freeze of 2026-10-21. `bin/harness` was re-frozen after each harness fix; the hashes are in `docs/results.md`.
- **Held-out evaluation.** See the "Held-out result" section of `docs/results.md` for the run, the labels and the scores.
- **Nothing should be running** once the close-out is done (check with `ps -eo args | grep -E "run_queue|bin/harness run|firecracker"`).

## What changed on 2026-11-25

- **Model API errors inside a 200 stream** (Gemini's 503 "high demand") are now infrastructure errors (`upstream_stream_error`), retried, and not counted as refusals. Before, they were scored as the agent giving up.
- **Rejected credentials** (upstream 401/403) are infrastructure errors (`upstream_auth`).
- **opencode in the VM** now declares the profile's model in its config (its offline model list is older than the host's), and profiles can set `provider_npm` and `model_options` for opencode (used for DeepSeek's own API with thinking off).
- **Queue tools:** `tools/queue/gemini_stream_gate.sh` (streaming probes, stricter than `gemini_gate.sh`), `tools/queue/stop_after_trial.sh` (pause a stage after the next trial result), and a fix to `watch_stage.sh` (it took old log lines for news).
- **Model used for the held-out run:** DeepSeek V4.1 Flash on DeepSeek's own API, thinking off (`profiles/retro-eval-deepseek-api.yaml`). Gemini flash-lite was overloaded, Gemini 3.5 Flash's free tier is 20 requests a day, opencode's free models refuse requests that come through the harness proxy, and the stored OpenCode Go and OpenRouter keys were rejected. Details in `docs/audit-report.md` (U12).

## Open items

1. **Human labels.** The held-out labels come from blind Claude subagents, not people (`docs/labelling-log.md`). A person labelling the same pack (`suites/detector-eval-heldout/pack`, keep `key.jsonl` away from the labeller) would make the detector figures much stronger.
2. **Dev pack labels.** `suites/detector-eval/pack` has no labels. Under the short plan only the held-out pack was labelled, and no detector fixes were made.
3. **Grader calibration.** `grader/` has never been compared with reference labels (`grader/FEEDBACK.md`).
4. **Not run:** see `docs/not-performed.md`.
5. **Credentials to renew if needed:** OpenCode Go (401 "Invalid credential"), OpenRouter (401 "User not found").
6. **ChemFlow evaluation** (`chemflow-eval/`, gitignored, not committed): separate work, see that folder's README.
7. **Not built:** an `--allow-web` option, per-call resource limits, shared toolchain layers, a Codex agent adapter, OpenCode 2 support.

## How to resume

```bash
go build -o bin/harness ./cmd/harness && bin/harness doctor
```

If `doctor` says `/dev/kvm` cannot be opened, see Troubleshooting in `README.md`. Long runs go through `tools/queue/run_queue.sh` with `ONLY=<stage>`, started detached, and are watched with `tools/queue/watch_stage.sh <stage>` in the background. Never print API keys; the harness reads them on the host from pi's and opencode's own stores.
