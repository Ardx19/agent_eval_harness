---
description: Review code against PLAN.md and log issues to REVIEW_LOG.md
---
Review the codebase against PLAN.md. Do not modify source code.
Check each plan item for completeness and correctness, then look for bugs,
edge cases, and security or performance problems in the related code.
Append findings to REVIEW_LOG.md using this format:
## [SEVERITY] Title
- File: path:line
- Plan item:
- Problem:
- Suggested fix:
Finish with a status table for every plan item. $ARGUMENTS