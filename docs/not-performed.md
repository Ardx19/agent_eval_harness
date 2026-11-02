# Tests not performed here

Tests left out of a batch for reasons that do not depend on the skill or the agent (host limits, infrastructure, quota). They say nothing about how the skill or any model behaves, and the grader must not count them as agent failures. Run them when the reason is gone and note the date.

| Test | Suite | Skipped on | Reason | Needed to run it | Status |
|---|---|---|---|---|---|
| p34 (both `max_steps: 3` and `best_route_metric: most_available` in one `synthesis_feasibility` call) | `suites/initial` (all three models: hy3, mimo, big-pickle) | 2026-10-07 | On the first attempt the tool was killed by the host kernel for lack of memory on all 4 attempts (`tool_oom`, attribution infra); the user chose to skip it for now | Enough host RAM for a full tool call (about 5.4 GB). WSL now has `memory=12GB`, so it may simply work. Run with `ONLY=... tools/queue/run_queue.sh` using a prompts file that holds only p34 | skipped, not yet retried |
| Break-test prompts on mimo-v2.6-flash-free and big-pickle (`suites/initial`) | `suites/initial` | 2026-10-07 | The free opencode APIs refused requests (`upstream`, runner cooldowns of 5 to 40 min); replaced by deepseek-v4.1-flash and gemini-3.5-flash-lite | The free quota back (or accept fewer models) | not run (partial: `runs/stopped/partial-r-20261007-113854`) |
| Break-test prompts on gemini-3.5-flash-lite and on paid mimo-v2.6-flash (`suites/initial`) | `suites/initial` | 2026-10-08 | Paid mimo: opencode-go API returned 502 after ~8 min on large requests (`upstream_unreachable`), 40 min cooldowns; gemini: stopped by the user to start grading with hy3 only | Healthy mimo API; or run `ONLY=initial-gemini SKIP=p34 tools/queue/run_queue.sh` (about 1 to 1.5 h) | not run |

Evidence for p34: `runs/stopped/partial-r-20261007-054133/p34/` (four attempts, all `tool_oom`).

The queue script leaves tasks out with `SKIP=p34 tools/queue/run_queue.sh`.
