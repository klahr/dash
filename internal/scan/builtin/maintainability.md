---
name: maintainability
title: Duplication and complexity
kind: maintainability
scope: repo
# Suggested: monthly, on the 15th at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 15 * *
---
Find duplicate code, overly complex code and needless complexity in {{path}}.

## Duplicate code

Look for logic that does the same thing in two or more places: copy-pasted
or nearly identical functions, request handlers, database queries,
validation, UI components and CSS blocks. Code that differs only in names,
literals or one small step counts too, if a parameter would cover the
difference.

For each finding, name every location (file and line), say where one shared
version should live and what it should be called, and what each copy would
pass to it. Report the first location as the file and line and list the
others in the details.

## Complexity

Look for functions and components that are too long, too deeply nested or
have too many branches to follow. For each, give a concrete way to split or
flatten it: extract named helpers (name them), return early instead of
nesting, replace a long switch or if chain with a table or a map, split a
component that does several jobs, or move a loop's body into a function.

## Needless complexity

Look for code that adds layers without adding anything:

- wrappers that only pass calls through to another function;
- abstractions and interfaces with just one implementation or one caller,
  unless they exist for tests or a boundary the code clearly needs;
- configuration, options and parameters nobody varies (every caller passes
  the same value);
- hand-written logic that the codebase or the standard library already
  provides: point to the existing helper and where it lives.

## Rules

Before suggesting a shared helper, search the whole repository, not only
{{path}}, for one that already exists; if there is one, the fix is to use
it, and say where it is.

Do not report:

- style preferences, formatting and naming nitpicks;
- small repeats (a few lines, or only two uses) that aren't worth a shared
  helper;
- duplication in tests, unless it hides what the test checks;
- generated code, vendored or third-party code, migrations, and files under
  node_modules, vendor or build output.

Each finding must be a change that pays for itself. In the details, say
what gets simpler (lines removed, places a future change has to touch,
branches a reader has to follow) and how risky the change is (how many
callers, whether tests cover them). Prefer a few strong findings over many
weak ones: when in doubt, leave it out.

Report each as type "issue", severity "low" (or "medium" when the duplicates
have already drifted apart, so one copy has a fix the others lack), with the
suggested fix naming the change. Fingerprint
"duplicate:<shared helper's name>", "complex:<qualified name>" or
"needless:<qualified name>" (for example "duplicate:internal/store.scanRows",
"complex:web/static/app.js:renderIssues").

Do not change any files.
