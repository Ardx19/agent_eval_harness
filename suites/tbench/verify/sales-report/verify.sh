#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check report-correct '[ "$(cat report.csv 2>/dev/null)" = '"'"'region,total
east,315.55
north,220.50
south,125.35
west,12.00'"'"' ]'
exit $fail
