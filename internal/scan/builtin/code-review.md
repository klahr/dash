---
name: code-review
title: Code review
kind: code_review
scope: since_last_scan
# Suggested: weekdays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 1-5
---
Review the code in {{path}} for correctness, the way the /code-review skill
does, and report what you find.

Look for real bugs: wrong logic, unhandled errors and edge cases, nil or
out-of-range access, resource leaks, concurrency problems, broken contracts
between callers and callees, and code that does not do what its name or
comment says. Report a maintainability problem only when it is likely to
cause bugs.

Do not report style, formatting or naming preferences. Each finding needs a
concrete input or situation that goes wrong.
