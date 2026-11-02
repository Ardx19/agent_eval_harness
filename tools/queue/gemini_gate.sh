#!/bin/bash
# Exit 0 when the Gemini free quota is really back: 3 probes in a row succeed, 20 s apart.
# Exit 1 if that has not happened by the deadline ($1, seconds since the epoch).
# Each probe costs one request. The key comes from pi and is never printed or put in argv.
deadline=${1:?usage: gemini_gate.sh <deadline-epoch-seconds>}
probe() {
  KEY=$(pi auth print-api-key --provider google --model gemini-3.5-flash-lite 2>/dev/null | tail -1)
  [ -n "$KEY" ] || return 2
  code=$(printf 'header = "x-goog-api-key: %s"\n' "$KEY" | curl -s -m 60 -K - -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"contents":[{"parts":[{"text":"Reply with the single word OK"}]}]}' 'https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash-lite:generateContent')
  unset KEY
  [ "$code" = 200 ]
}
while [ "$(date +%s)" -lt "$deadline" ]; do
  if probe && { sleep 20; probe; } && { sleep 20; probe; }; then exit 0; fi
  sleep 1200
done
exit 1
