#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check contacts-correct 'python3 - <<'"'"'PYEND'"'"'
import json,sys
d=json.load(open('"'"'contacts.json'"'"'))
sys.exit(0 if d==json.loads('"'"'{"emails": ["dana.lee@example.com", "ops-team@example.org", "sales@vendor.example.net"], "phones": ["2125550107", "4155550132", "4155550199"]}'"'"') else 1)
PYEND'
exit $fail
