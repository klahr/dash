---
name: code-review
title: Code review
kind: code_review
scope: since_last_scan
# Suggested: weekdays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 1-5
---
Review the code in {{path}} for correctness: find real bugs, and report
them.

## How to work

For each change, read the changed code and enough around it to know what
it is meant to do: its callers, the functions it calls, its tests, and the
comments and docs that describe it. Then look for inputs and situations
where it does something else. Confirm each bug by following the code: name
the input or sequence of events and trace it to the wrong result.

## What to check

- Wrong logic: inverted or missing conditions, off-by-one errors, wrong
  operator or variable, a case of a switch that is missing or falls
  through, and code that does not do what its name, comment or docs say.
- Errors and edge cases: errors that are ignored, swallowed or lost,
  error paths that leave things half done, and empty, zero, negative,
  very large, duplicate or missing inputs.
- Nil, null and bounds: dereferencing a value that can be nil or
  undefined, indexing out of range, and integer overflow or truncation.
- Resources: files, connections, transactions, locks, goroutines,
  timers and subscriptions that are not closed or released on every path,
  including the error paths.
- Concurrency: data races on shared state, missing or inconsistent
  locking, deadlocks, lost updates (read, modify, write without a lock or
  transaction), and work started after the context or request is gone.
- Broken contracts: a caller that passes what the callee doesn't accept
  or ignores what it returns, a changed function whose other callers were
  not updated, and an interface implementation that breaks its
  documented behaviour.
- Data: wrong units or time zones, lost precision, encoding mistakes,
  and queries or migrations that change more rows than intended.

## What not to report

- Style, formatting, naming and wording preferences.
- Maintainability, unless it is likely to cause a bug (two copies that
  have already drifted apart, say); the maintainability scan covers the
  rest.
- Security vulnerabilities and performance problems: the security and
  performance scans cover them. Report a bug that is also one only once,
  here, if it is wrong behaviour for ordinary users.
- Missing tests: the test-gaps scan covers them.
- Generated code, vendored or third-party code, and problems you can
  only guess at without an input that triggers them.

When in doubt, leave it out.

## Reporting

Report each as type "bug", with the file and line of the faulty code. In
the details, give the input or situation that goes wrong, what happens,
and what should happen instead. The suggested fix is the specific change.
Use the shared severity scale below: high for a bug ordinary use hits in a
main feature, medium for an edge case or a secondary feature, low for
something with little effect.

Fingerprint "<kind>:<qualified function>", with kind one of logic,
error-handling, nil, bounds, overflow, leak, race, deadlock, contract or
data: for example "nil:internal/store.(*Store).GetTodo",
"leak:web/static/app.js:openSocket".

Do not change any files.
