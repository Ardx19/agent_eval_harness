#!/bin/bash
# The long batch of evaluation runs, one stage at a time (so memory-heavy stages never overlap).
#
#   tools/queue/run_queue.sh                                  run the default stages
#   ONLY=gemini-break,gemini-smoke tools/queue/run_queue.sh   run just these stages (opt-in and gated stages need this)
#   DRY=1 ONLY=... tools/queue/run_queue.sh                   print what would run; no trials, no Gemini probes
#   SKIP=p34 tools/queue/run_queue.sh                         leave these task ids out of every stage (docs/not-performed.md)
#   TASKS=p42,p43 ONLY=initial-hy3 tools/queue/run_queue.sh   run only these task ids
#
# Stage kinds:
#   default  run when ONLY is unset
#   opt      run only when named in ONLY (superseded or not part of the current plan)
#   gated    wait for Gemini's free quota (tools/queue/gemini_gate.sh) before starting
#
# Settings (environment):
#   STAGE_CAP_S   wall-clock cap per stage in seconds (default 25200 = 7 h); the stage's whole process group is stopped
#   KILL_GRACE_S  seconds between TERM and KILL when a cap is hit (default 60)
#   GATE_WAIT_S   how long a gated stage waits for the quota (default 28800 = 8 h)
#
# Progress: runs/queue/queue.log (ends with "done"); each stage's console output: runs/queue/<stage>.out
# Filtered prompt copies: runs/queue/filtered/<stage>/ (the source prompts are never modified).
# Needs: bin/harness (go build -o bin/harness ./cmd/harness) and the guest image (images/build.sh).
cd "$(dirname "$0")/../.."
export PATH=$PATH:/usr/local/go/bin:/usr/sbin
H=./bin/harness
LOG=runs/queue/queue.log
EV=suites/detector-eval
STAGE_CAP_S=${STAGE_CAP_S:-25200}
KILL_GRACE_S=${KILL_GRACE_S:-60}
GATE_WAIT_S=${GATE_WAIT_S:-28800}
DRY=${DRY:-}

KNOWN=" initial-hy3 initial-mimo initial-pickle initial-mimogo initial-deepseek initial-gemini gemini-break gemini-smoke swe-mimo-rest mimo-extra pickle-main pickle-extra swe-pickle gemini-rest gemini-heldout report "
OPT=" initial-deepseek initial-gemini gemini-break gemini-smoke gemini-heldout "
GATED=" gemini-break gemini-smoke gemini-rest gemini-heldout "

say() { if [ -n "$DRY" ]; then echo "$*"; else mkdir -p runs/queue; echo "$(date '+%F %T') $*" >> "$LOG"; fi; }
fail() { echo "queue: $*" >&2; say "ERROR: $*"; exit "${2:-2}"; }

# An unknown name in ONLY used to be skipped silently; now it stops the queue before anything runs.
for s in ${ONLY//,/ }; do
  case "$KNOWN" in *" $s "*) ;; *) fail "unknown stage in ONLY: $s (known:$KNOWN)" ;; esac
done

want() { # stage kind
  if [ -n "$ONLY" ]; then [[ ",$ONLY," == *",$1,"* ]]; return; fi
  [ "$2" = default ]
}

# run_capped: the stage's command runs under timeout(1), which stops the whole process group
# at the cap (so the harness's firecracker and tool children do not outlive it).
run_capped() {
  timeout --kill-after="$KILL_GRACE_S" "$STAGE_CAP_S" "$@"
}

# stage NAME PROFILE PROMPTS OUTDIR N BUDGET [TASK_IDS]
stage() {
  local name=$1 profile=$2 prompts=$3 out=$4 n=$5 budget=$6 ids=${7:-}
  local kind=default gate=
  case "$OPT" in *" $name "*) kind=opt ;; esac
  want "$name" "$kind" || return 0
  [[ "$GATED" == *" $name "* ]] && gate=", waits for Gemini quota"
  if [ -n "$DRY" ]; then
    echo "would run: $name (profile $profile, prompts $prompts, out $out, n $n, budget \$$budget${ids:+, tasks $ids}${SKIP:+, skip $SKIP}${TASKS:+, only $TASKS}$gate)"
    return 0
  fi
  # 1. Prepare the prompts file and check its paths. This runs before any Gemini probe, so a
  #    stage that cannot start (for example held-out prompts not yet approved) costs no requests.
  local fdir="runs/queue/filtered/$name" src_dir fprompts
  src_dir=$(cd "$(dirname "$prompts")" && pwd)
  fprompts="$fdir/$(basename "$prompts")"
  if [ -n "$SKIP" ] || [ -n "$TASKS" ] || [ -n "$ids" ]; then
    # A filtered copy lives outside suites/. Every sibling of the source is linked into it, so
    # input, workspace and verify paths (relative to the prompts file) still resolve.
    rm -rf "$fdir" && mkdir -p "$fdir"
    local e b
    for e in "$src_dir"/*; do
      b=$(basename "$e")
      [ "$b" = "$(basename "$prompts")" ] && continue
      ln -s "$e" "$fdir/$b"
    done
    local only_args=()
    [ -n "$ids" ] && only_args+=(--only "$ids")
    [ -n "$TASKS" ] && only_args+=(--only "$TASKS")
    local kept
    kept=$(python3 tools/queue/filter_prompts.py "$prompts" "$fprompts" --skip "$SKIP" "${only_args[@]}") || fail "could not filter $prompts for $name"
    say "stage $name prompts: $kept (skip: ${SKIP:-none}; only: ${ids:-all}${TASKS:+; env only: $TASKS})"
    prompts=$fprompts
  fi
  mkdir -p runs/queue
  if ! python3 tools/queue/check_prompts.py "$prompts" > "runs/queue/$name.check" 2>&1; then
    cat "runs/queue/$name.check" >&2
    fail "stage $name stopped before any trial: its prompts file names a path that does not exist (see runs/queue/$name.check)" 3
  fi
  # 2. Gated stages wait for the Gemini quota (each probe is one request).
  if [ -n "$gate" ]; then
    say "waiting (up to ${GATE_WAIT_S} s) for the Gemini quota before $name"
    if ! tools/queue/gemini_gate.sh $(( $(date +%s) + GATE_WAIT_S )); then
      say "Gemini quota did not come back; stage $name skipped"
      return 0
    fi
  fi
  # 3. Run, under the stage cap.
  say "stage $name"
  run_capped "$H" run --profile "profiles/$profile.yaml" --prompts "$prompts" --out "$out" --n "$n" --budget-usd "$budget" --retries 3 --skip-preflight > "runs/queue/$name.out" 2>&1
  local rc=$?
  if [ "$rc" = 124 ] || [ "$rc" = 137 ]; then say "stage $name hit its ${STAGE_CAP_S} s cap and was stopped (exit $rc)"; fi
  say "stage $name finished (exit $rc)"
}

say "queue started${ONLY:+ (only: $ONLY)}${DRY:+ (dry run)}"

# The break-test prompts on the models the grader is built and checked against.
stage initial-hy3     retro-hy3      suites/initial/prompts.jsonl            runs/initial 1 1.5
stage initial-mimo    retro-mimo     suites/initial/prompts.jsonl            runs/initial 1 1
stage initial-pickle  retro-pickle   suites/initial/prompts.jsonl            runs/initial 1 1
stage initial-mimogo  retro-mimo-go  suites/initial/prompts.jsonl            runs/initial 1 1
# Opt-in: DeepSeek is not part of the current plan; initial-gemini is superseded by gemini-break.
stage initial-deepseek retro-deepseek suites/initial/prompts.jsonl            runs/initial 1 1.5
stage initial-gemini  retro-suite    suites/initial/prompts.jsonl            runs/initial 1 2.5
# Gemini break-tests (fresh calibration set; run with SKIP=p34) and the two-prompt smoke test.
stage gemini-break    retro-eval     suites/initial/prompts.jsonl            runs/initial 1 2.5
stage gemini-smoke    retro-eval     suites/initial/prompts.jsonl            runs/smoke 1 0.5 p35,p38
# Coding tasks and detector-evaluation traces.
stage swe-mimo-rest   swe-mimo       suites/swe-lite/prompts.rest.jsonl      runs 3 1
stage mimo-extra      retro-mimo     suites/detector-eval-extra/prompts.jsonl runs/detector-eval 1 1
stage pickle-main     retro-pickle   $EV/prompts.jsonl                       runs/detector-eval 1 1
stage pickle-extra    retro-pickle   suites/detector-eval-extra/prompts.jsonl runs/detector-eval 1 1
stage swe-pickle      swe-pickle     suites/swe-lite/prompts.jsonl           runs 3 1
# Gemini detector traces; gemini-heldout needs the approved held-out prompts (plan U11).
stage gemini-rest     retro-eval     $EV/prompts.gemini-rest.jsonl           runs/detector-eval 1 2.5
stage gemini-heldout  retro-eval     suites/detector-eval-heldout/prompts.jsonl runs/detector-eval-heldout 1 2.5

if want report default; then
  if [ -n "$DRY" ]; then
    echo "would run: report (grade every run; build a review pack in runs/queue/packs/, existing packs are not touched)"
  else
    say "grading runs"
    for d in runs/r-* runs/detector-eval/r-* runs/detector-eval-heldout/r-* runs/initial/r-*; do
      [ -f "$d/summary.json" ] && $H grade --run "$d" > /dev/null 2>&1
    done
    # The report writes a new pack and never deletes one: labels placed in a pack survive.
    pack="runs/queue/packs/pack-$(date +%Y%m%d-%H%M%S)"
    say "building a review pack in $pack (existing packs are not touched)"
    mkdir -p "$(dirname "$pack")"
    $H review-pack --out "$pack" $(ls -d runs/detector-eval/r-* 2>/dev/null) > runs/queue/review-pack.out 2>&1
  fi
fi
if [ -n "$DRY" ]; then exit 0; fi
say "all stages done"
echo done >> "$LOG"
