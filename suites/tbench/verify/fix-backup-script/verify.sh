#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check copies-file-with-spaces 'd=$(mktemp -d) && cd $d && echo hello > "my file.txt" && bash /home/dev/project/backup.sh "my file.txt" "out dir" >/dev/null && [ "$(cat "out dir/my file.txt")" = hello ]'
check creates-backup-dir 'd=$(mktemp -d) && cd $d && echo a > f && bash /home/dev/project/backup.sh f new/nested/dir >/dev/null && [ -f new/nested/dir/f ]'
check missing-source-fails 'd=$(mktemp -d) && cd $d && ! bash /home/dev/project/backup.sh nope out >/dev/null 2>err && [ -s err ]'
exit $fail
