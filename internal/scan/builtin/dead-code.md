---
name: dead-code
title: Dead code and stale TODOs
kind: dead_code
scope: repo
# Suggested: monthly, on the 1st at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 1 * *
---
Find dead code and stale TODOs in {{path}}.

## Unused code

Look for things nothing uses any more: functions and methods, types,
constants and variables, command-line flags, HTTP routes and handlers,
configuration keys and environment variables, CSS classes, and whole files
(source files, templates, scripts, assets) that nothing includes, imports
or loads.

Before reporting anything as unused, verify it by searching the whole
repository, not only {{path}}: every language, the tests, scripts, build and
CI files, docs, templates and static assets. A name can be used without a
direct call, so also search for it as a string and in pieces:

- reflection and dynamic dispatch (method or field names looked up by
  string, `reflect`, `getattr`, `send`, `obj[name]`), dependency injection
  and plugin or handler registries;
- templates (HTML, text, email), where functions, fields and methods are
  called by name;
- serialization: struct tags, JSON / YAML / TOML keys, database columns and
  ORM mappings, protobuf or OpenAPI definitions;
- routes called from the frontend, other services or docs (fetch URLs, API
  clients, links), and flags or config keys set in scripts, Dockerfiles,
  deploy files, CI, README or docs;
- interface implementations, callbacks, `init` functions, `main` packages,
  test helpers, build tags and generated code, and names built at run time
  ("handle" + action).

Do not report:

- the exported API of a library: a package meant to be imported by other
  projects (a published module or package, a `pkg/` directory, an SDK) may
  have callers outside this repository. Only report its unexported code, or
  exported code in an application's internal packages or `main`.
- generated code, vendored or third-party code, and files under
  node_modules, vendor or build output;
- code that is only unused on some platforms or build tags, and code kept on
  purpose (a comment says why, or it is behind a feature flag in use).

When in doubt, leave it out: a false "unused" costs a reviewer more than a
missed one. In the details, say what you searched for and where, so a
reviewer can check it. Report each as type "issue", severity "low" (or
"medium" for a whole unused file, route or flag that adds attack surface or
confuses users), with the definition as the file and line, and the
suggested fix "Delete …" naming what else goes with it (its tests, docs,
config). Fingerprint "unused:<kind>:<qualified name>" (for example
"unused:func:internal/store.oldQuery", "unused:file:web/static/old.css").

## Stale TODOs

Then find TODO, FIXME, XXX and HACK comments. For each, use git_blame on
its line to find when it was written. Report the ones older than 90 days
that still describe work to do (check that the code has not already done
it): they are worth turning into issues. Put the comment's text in the
title, its age, author date and commit, and what it would take in the
details, and the comment's file and line. Type "issue", severity "low", or
the severity the work itself deserves when the comment marks a known bug
(FIXME, "this breaks when …"). Fingerprint "todo:<file>:<the comment's text,
trimmed>" so it survives the line moving. A TODO that is no longer true (the
work is done) is a finding too: suggest deleting the comment.

Do not change any files.
