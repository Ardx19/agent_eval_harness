#!/bin/bash
# Stop a running queue stage as soon as the next trial result is printed, so the run can be
# resumed later with SKIP=<finished task ids>. The trial that has just started is cancelled
# (it is recorded as interrupted and must be re-run).
#   tools/queue/stop_after_trial.sh STAGE
cd "$(dirname "$0")/../.." || exit 2
stage=${1:?usage: stop_after_trial.sh STAGE}
out=runs/queue/$stage.out
log=runs/queue/stop-after.log
pat='^[A-Za-z0-9_.-]+ +trial [0-9]+  [a-z_]+ +steps'
count() { local n; n=$(grep -cE "$pat" "$out" 2>/dev/null); echo "${n:-0}"; }  # grep -c prints 0 itself on no match
start=$(count)
echo "$(date '+%F %T') waiting for trial result number $((start + 1)) of $stage" >> "$log"
while [ "$(count)" -le "$start" ]; do
  pgrep -f "tools/queue/run_queue.sh" > /dev/null || { echo "$(date '+%F %T') queue already gone" >> "$log"; exit 0; }
  sleep 5
done
grep -E "$pat" "$out" | tail -1 >> "$log"
pkill -TERM -f "tools/queue/run_queue.sh"
pkill -INT -f "bin/harness run --profile"
echo "$(date '+%F %T') stop sent" >> "$log"
