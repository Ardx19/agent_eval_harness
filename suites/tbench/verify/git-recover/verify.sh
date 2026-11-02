#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check config-restored '[ "$(cat config.yaml 2>/dev/null)" = "$(printf "retries: 3\ntimeout: 30\nmode: strict")" ]'
check threshold-fixed 'python3 - <<'"'"'PYEND'"'"'
import app,sys
sys.exit(0 if app.allowed(50) and not app.allowed(49) else 1)
PYEND'
exit $fail
