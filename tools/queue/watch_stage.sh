#!/bin/bash
# Wait on one queue stage and exit with a one-line reason at the first thing worth a look.
# Run it in the background so whoever started it is woken when it exits:
#   done     the queue logged "all stages done"
#   gone     no queue process is left and the queue never logged "all stages done"
#   problem  a trial ended infra_error, oom, refused or budget_exceeded; the run stopped early;
#            the queue logged an error, a stage cap or a nonzero stage exit; a Go panic
#   stalled  the running stage has written nothing for STALL_S seconds
#   quota    the queue has waited QUOTA_S seconds for the Gemini quota (reported once)
#   memory   host MemAvailable under MEM_MIN_MIB on two checks in a row
# Lines already seen are kept in $QDIR/watch-<stage>.state, so a re-armed watcher reports only
# what is new.
#
#   tools/queue/watch_stage.sh STAGE
#
# Settings (environment): QDIR (default runs/queue), POLL_S (30), STALL_S (3600), QUOTA_S (1800),
# MEM_MIN_MIB (800), QUEUE_MATCH (pgrep -f pattern of the queue process; default
# tools/queue/run_queue.sh).
cd "$(dirname "$0")/../.." || exit 2
stage=${1:?usage: watch_stage.sh STAGE}
QDIR=${QDIR:-runs/queue}
POLL_S=${POLL_S:-30}
STALL_S=${STALL_S:-3600}
QUOTA_S=${QUOTA_S:-1800}
MEM_MIN_MIB=${MEM_MIN_MIB:-800}
QUEUE_MATCH=${QUEUE_MATCH:-tools/queue/run_queue.sh}
log=$QDIR/queue.log
out=$QDIR/$stage.out
state=$QDIR/watch-$stage.state
out_lines=0 log_lines=0 quota_reported=0
# shellcheck disable=SC1090
[ -f "$state" ] && . "$state"
low=0

save() { printf 'out_lines=%s\nlog_lines=%s\nquota_reported=%s\n' "$out_lines" "$log_lines" "$quota_reported" > "$state"; }
finish() { # REASON MESSAGE [LINES]
  save
  echo "$1: $2"
  [ -n "$3" ] && printf '%s\n' "$3" | tail -n 20
  echo "(stage output $out; queue log $log; re-arm: tools/queue/watch_stage.sh $stage)"
  exit 0
}
lines() { if [ -f "$1" ]; then wc -l < "$1"; else echo 0; fi; }
# between FILE SEEN N: lines SEEN+1 to N. Lines written after N are read on the next pass.
between() { [ -f "$1" ] && [ "$3" -gt "$2" ] && sed -n "$(( $2 + 1 )),$3p" "$1"; }

check_log() {
  local n fresh bad
  n=$(lines "$log"); [ "$n" -lt "$log_lines" ] && log_lines=0
  fresh=$(between "$log" "$log_lines" "$n"); log_lines=$n
  bad=$(printf '%s\n' "$fresh" | grep -E 'ERROR|hit its .* cap|did not come back|finished \(exit [1-9]')
  [ -n "$bad" ] && finish problem "the queue log reports a problem" "$bad"
  printf '%s\n' "$fresh" | grep -q 'all stages done' && finish done "the queue finished" "$(printf '%s\n' "$fresh" | grep -E " stage $stage|all stages done")"
}

check_out() {
  local n fresh bad
  n=$(lines "$out"); [ "$n" -lt "$out_lines" ] && out_lines=0
  fresh=$(between "$out" "$out_lines" "$n"); out_lines=$n
  bad=$(printf '%s\n' "$fresh" | grep -E 'trial +[0-9]+ +(infra_error|oom|refused|budget_exceeded)( |$)|stopped early|panic:|^run: ')
  [ -n "$bad" ] && finish problem "the stage reported a problem" "$bad"
}

while true; do
  check_log
  check_out
  last=$(tail -n 1 "$log" 2>/dev/null)
  now=$(date +%s)
  if [[ "$last" == *" stage $stage" ]] && [ -f "$out" ]; then
    idle=$(( now - $(stat -c %Y "$out") ))
    [ "$idle" -ge "$STALL_S" ] && finish stalled "stage $stage has written nothing for $(( idle / 60 )) min"
  fi
  if [ "$quota_reported" = 0 ] && [[ "$last" == *"for the Gemini quota before $stage" ]]; then
    since=$(date -d "${last:0:19}" +%s 2>/dev/null || echo "$now")
    if [ $(( now - since )) -ge "$QUOTA_S" ]; then
      quota_reported=1
      finish quota "still waiting for the Gemini quota after $(( (now - since) / 60 )) min" "$last"
    fi
  fi
  avail=$(awk '/^MemAvailable:/ {print int($2 / 1024)}' /proc/meminfo)
  if [ "$avail" -lt "$MEM_MIN_MIB" ]; then low=$(( low + 1 )); else low=0; fi
  [ "$low" -ge 2 ] && finish memory "host memory available is ${avail} MiB"
  if ! pgrep -f "$QUEUE_MATCH" > /dev/null; then
    sleep 2
    check_log
    finish gone "no queue process is running and the queue log has no \"all stages done\""
  fi
  save
  sleep "$POLL_S"
done
