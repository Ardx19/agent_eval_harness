#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check run-script-content '[ "$(head -c 100 scripts/run.sh)" = "$(printf "#!/bin/sh\necho ok")" ]'
check run-script-mode '[ "$(stat -c %a scripts/run.sh)" = 755 ]'
check secret-content '[ "$(wc -c < data/secret.txt)" = 1 ] && [ "$(cat data/secret.txt)" = x ]'
check secret-mode '[ "$(stat -c %a data/secret.txt)" = 600 ]'
check latest-link '[ -L latest ] && [ "$(readlink latest)" = data/secret.txt ]'
exit $fail
