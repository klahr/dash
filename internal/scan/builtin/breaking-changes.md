---
name: breaking-changes
title: Breaking changes
kind: breaking_changes
scope: since_main
# No schedule: it compares the checkout's branch with the default branch,
# so run it on a branch before merging it.
---
Check what the checkout's branch changes in {{path}} compared with the
default branch for changes that break the code's callers or users. Start
from the diff since {{since}} and read the code around each change to see
who depends on it.

Report changes that break someone who relies on the code as it is on the
default branch:

- public API: exported functions, types, methods and fields that were
  removed or renamed, or whose parameters, return values or behaviour
  changed in a way existing callers would notice;
- HTTP endpoints: removed or renamed routes, changed methods, required
  request fields that are new, response fields that were removed, renamed
  or changed type, and changed status codes or error formats;
- database schema: changes to tables, columns or indexes with no migration
  that applies them, and migrations that destroy data (dropping or
  narrowing a column or table, rewriting values) or that an existing
  database can't run;
- command-line flags, config keys and environment variables that were
  removed or renamed, or whose meaning or default changed;
- file formats the code reads or writes (saved state, exports, caches,
  wire messages): files written by the default branch's version that the
  new code can no longer read, or new files older readers would misread.

Each finding names what breaks and who is affected: the file and line of
the change, in the details the old and the new form quoted briefly and the
callers, clients or stored data that depend on the old one. The suggested
fix keeps the old form working (an alias, a default, a migration, a
deprecation period) or says what has to be announced. Use
"<kind of interface>:<old name>" as the fingerprint, e.g. "flag:-port" or
"endpoint:GET /api/items".

Only report a break you have confirmed against the default branch's code.
Leave out changes to unexported or internal code that every caller was
updated with, additions that keep the old behaviour, and interfaces the
project documents as experimental or unstable. Do not change any files.
