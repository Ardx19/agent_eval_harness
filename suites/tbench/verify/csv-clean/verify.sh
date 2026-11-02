#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check clean-script-exists '[ -f clean.py ]'
check output-correct '[ "$(cat people_clean.csv 2>/dev/null)" = '"'"'name,age,city
Ann,29,Rome
Cy,40,Lima
Dee,35,Cairo
Zed,31,Oslo'"'"' ]'
check script-reproduces-output 'd=$(mktemp -d) && cp people.csv clean.py $d/ && cd $d && python3 clean.py && cmp -s people_clean.csv /home/dev/project/people_clean.csv'
exit $fail
