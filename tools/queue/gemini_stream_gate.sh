#!/bin/bash
# Exit 0 when Gemini answers real streaming requests again: N probes in a row (default 5,
# GAP_S apart, default 60 s) each return a 200 stream with a non-thought answer and no
# error event inside the stream. Exit 1 if that has not happened by the deadline ($1,
# seconds since the epoch).
#
# Why: tools/queue/gemini_gate.sh sends one tiny non-streaming request, which succeeds
# while the model is overloaded. On 2026-11-25 Gemini answered such probes but broke
# most agent requests with a 503 "high demand" event inside the 200 stream
# (docs/audit-report.md, U12). This gate asks the same way the agent does: streaming,
# with thought summaries on.
#
# Each probe costs one request. The key comes from pi and is never printed or put in argv.
deadline=${1:?usage: gemini_stream_gate.sh <deadline-epoch-seconds>}
N=${N:-5}
GAP_S=${GAP_S:-60}
WAIT_S=${WAIT_S:-900}
MODEL=${MODEL:-gemini-3.5-flash-lite}
LOG=${LOG:-runs/queue/stream-gate.log}
cd "$(dirname "$0")/../.." || exit 2
mkdir -p "$(dirname "$LOG")"
body='{"contents":[{"role":"user","parts":[{"text":"In about 120 words, explain why a retrosynthesis route with fewer steps is usually preferred, and name one exception."}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}'
probe() {
  local KEY out code
  KEY=$(pi auth print-api-key --provider google --model "$MODEL" 2>/dev/null | tail -1)
  [ -n "$KEY" ] || return 2
  out=$(mktemp)
  code=$(printf 'header = "x-goog-api-key: %s"\n' "$KEY" | curl -s -m 120 -K - -o "$out" -w '%{http_code}' -H 'Content-Type: application/json' \
    -d "$body" "https://generativelanguage.googleapis.com/v1beta/models/$MODEL:streamGenerateContent?alt=sse")
  unset KEY
  local ok=1
  if [ "$code" = 200 ] && ! grep -q '"error"' "$out" && grep -v '"thought": *true' "$out" | grep -q '"text"'; then ok=0; fi
  echo "$(date '+%F %T') probe http=$code $( [ $ok = 0 ] && echo ok || echo bad: $(grep -o '"status": *"[A-Z_]*"' "$out" | head -1))" >> "$LOG"
  rm -f "$out"
  return $ok
}
while [ "$(date +%s)" -lt "$deadline" ]; do
  good=0
  while [ $good -lt "$N" ] && probe; do
    good=$((good + 1))
    [ $good -lt "$N" ] && sleep "$GAP_S"
  done
  if [ $good -ge "$N" ]; then echo "$(date '+%F %T') gate open ($N in a row)" >> "$LOG"; exit 0; fi
  sleep "$WAIT_S"
done
echo "$(date '+%F %T') gate deadline passed" >> "$LOG"
exit 1
