#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check make-all-works 'd=$(mktemp -d) && cp -r Makefile src $d/ && cd $d && make all >/dev/null 2>&1 && [ "$(cat dist/bundle.txt)" = "$(printf "header\nbody\nfooter")" ]'
check make-clean-works 'd=$(mktemp -d) && cp -r Makefile src $d/ && cd $d && make all >/dev/null 2>&1 && make clean >/dev/null 2>&1 && [ ! -e dist ]'
check sources-untouched '[ "$(cat src/10-header.txt)" = header ] && [ "$(cat src/20-body.txt)" = body ] && [ "$(cat src/30-footer.txt)" = footer ]'
exit $fail
