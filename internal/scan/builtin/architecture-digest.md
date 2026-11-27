---
name: architecture-digest
title: Architecture digest
kind: digest
scope: since_last_scan
# Suggested: Fridays at 15:00, about what was merged since the last run
# (skipped when nothing was). Built-in schedules are off until turned on.
schedule: 0 15 * * 5
# The first run looks at the last week, not all of history.
first_run_days: 7
---
Write a short digest of what changed in {{path}} on the default branch, for
people who no longer read every change because agents write most of the
code. They want to keep the big picture: how the codebase is shaped and
where it is going. This is a digest, not a changelog: leave out bug fixes,
small features, refactors that keep the shape, tests, docs and dependency
bumps, unless one of them changes the picture below.

Read the diff and the commit messages; read the surrounding code only to
understand a change. Report what changed in the code's shape:

- packages, modules or services added or removed, and what each is for;
- new dependencies (libraries, services, tools), and what they are used for;
- public APIs, HTTP routes, CLI flags, config keys, environment variables,
  database schemas and file or wire formats added, changed or removed;
- a new pattern (a new way of doing errors, state, concurrency, storage,
  UI), or an existing one used in a new place or a new way;
- code that grew a lot: a file, package or function that is much bigger now.

Then, under "Worth a look", at most three notes for a person to read
closely: a place where the design drifted from how the rest of the code
does it, the same thing built twice by different tasks, or a big or risky
change that looks like it got a quick review (much code, few tests, a large
commit with a short message). Say why, and where. These are notes, not
problems to file: leave "findings" empty unless something is clearly a bug.

Write it like this, in Markdown:

- `## What changed`: a list, one item per change, most important first, each
  one or two sentences, naming the tasks that caused it (as links) and the
  main file or package. Group small related changes into one item. Ten items
  at most; leave out the rest.
- `## Worth a look`: the notes, if any; leave the heading out when there are
  none.

If nothing notable changed, the whole digest is one line saying so (for
example "Nothing notable: 4 small fixes in internal/api."), with no headings,
and "headline" is empty.

Do not change any files.
