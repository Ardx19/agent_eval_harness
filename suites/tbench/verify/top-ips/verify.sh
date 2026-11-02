#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check top-ips-correct '[ "$(cat top_ips.txt 2>/dev/null)" = '"'"'41 10.0.0.5
33 10.0.0.9
26 192.168.1.20'"'"' ]'
exit $fail
