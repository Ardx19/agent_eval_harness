#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check merge-script-exists '[ -f merge.py ]'
check effective-correct 'python3 - <<'"'"'PYEND'"'"'
import json,sys
sys.exit(0 if json.load(open('"'"'effective.json'"'"'))==json.loads('"'"'{"server": {"host": "127.0.0.1", "port": 443, "tls": {"enabled": true, "cert": "default.pem"}}, "log": {"level": "debug", "files": ["prod.log", "audit.log"]}, "features": ["z"]}'"'"') else 1)
PYEND'
exit $fail
