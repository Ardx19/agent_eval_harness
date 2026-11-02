#!/bin/bash
cd /home/dev/project
fail=0
check() { name=$1; shift; if ( eval "$@" ) >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fail=1; fi; }
check slug-0 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'Hello, World!'"'"')=='"'"'hello-world'"'"' else 1)
PYEND'
check slug-1 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'  Multiple   spaces  '"'"')=='"'"'multiple-spaces'"'"' else 1)
PYEND'
check slug-2 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'Café au lait'"'"')=='"'"'cafe-au-lait'"'"' else 1)
PYEND'
check slug-3 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'--already-slug--'"'"')=='"'"'already-slug'"'"' else 1)
PYEND'
check slug-4 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'A/B testing: 50% off'"'"')=='"'"'a-b-testing-50-off'"'"' else 1)
PYEND'
check slug-5 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"''"'"')=='"'"''"'"' else 1)
PYEND'
check slug-6 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'!!!'"'"')=='"'"''"'"' else 1)
PYEND'
check slug-7 'python3 - <<'"'"'PYEND'"'"'
import sys
from slugify import slugify
sys.exit(0 if slugify('"'"'Ünïcödé Text'"'"')=='"'"'unicode-text'"'"' else 1)
PYEND'
exit $fail
