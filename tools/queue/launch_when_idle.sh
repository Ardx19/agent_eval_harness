#!/bin/bash
# Wait until no process matches $WAIT_FOR (two checks 30 s apart), then start the queue.
#   WAIT_FOR=bulwark MAX_WAIT_S=7200 SKIP=p34 ONLY=initial-hy3,initial-mimo,initial-pickle tools/queue/launch_when_idle.sh
# Progress goes to runs/queue/queue.log. Never starts a second queue if one is already running.
cd "$(dirname "$0")/../.."
LOG=runs/queue/queue.log
say() { echo "$(date '+%F %T') $*" >> "$LOG"; }
busy() { ps -eo args | grep -E "$1" | grep -v grep | grep -vq "launch_when_idle"; }
say "launcher: waiting for '[${WAIT_FOR:0:1}]${WAIT_FOR:1}' to finish (max ${MAX_WAIT_S:-7200}s)"
start=$(date +%s); pat="[${WAIT_FOR:0:1}]${WAIT_FOR:1}"
while true; do
  if ! busy "$pat"; then sleep 30; busy "$pat" || break; fi
  if [ $(( $(date +%s) - start )) -ge "${MAX_WAIT_S:-7200}" ]; then say "launcher: waited too long, starting anyway"; break; fi
  sleep 30
done
if ps -eo args | grep -E "tools/queue/run_queue.sh|\./bin/harness run" | grep -v grep | grep -q .; then say "launcher: a queue is already running, not starting another"; exit 0; fi
say "launcher: machine idle (load $(cut -d' ' -f1 /proc/loadavg)), memory $(free -m | awk 'NR==2{print $7}') MB available; starting the queue"
exec tools/queue/run_queue.sh
