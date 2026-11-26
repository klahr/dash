---
name: docs-drift
title: Docs drift
kind: docs
scope: since_last_scan
# Suggested: Mondays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 1
---
Check that the documentation of the project in {{path}} still matches the
code. Read the documentation and compare what it says with what the code
actually does:

- the README, the files under docs/, CLAUDE.md and AGENTS.md;
- CONTRIBUTING (build, test and release steps that must still work);
- the CHANGELOG's latest entries (released or unreleased) that describe
  behaviour, flags or defaults;
- OpenAPI / Swagger specs (openapi.yaml, swagger.json, …): paths,
  methods, parameters, request and response fields, and status codes
  against the routes and handlers;
- command-line help text (flag and command descriptions, usage strings,
  `--help` output written in the code) against what the flags do and their
  defaults;
- `.env.example`, `.env.sample` and sample config files: variables and
  keys the code no longer reads, and ones it requires that they leave out.

Report documentation that no longer holds: command-line flags, HTTP
endpoints, config keys, environment variables and commands that were
renamed or removed, defaults and behaviour that changed, and examples that
would not work if someone ran or copied them as written. Start from what
changed in the code and look for the docs that describe it, then check the
docs that changed against the code.

Each finding cites both sides: the doc file and line as the finding's file
and line, and in the details the code (file and line) that contradicts it,
quoting both briefly. The suggested fix is the corrected doc text. Use
"<doc file>:<the documented name>" as the fingerprint.

Report each as type "issue". Severity: "medium" for a documented command,
setup step, example or API call that fails or does harm when followed as
written, "low" otherwise.

Only report a contradiction you have confirmed in the code. Leave out
typos, wording and style, docs that are merely incomplete, and features
that are documented as planned. Leave a CHANGELOG entry for an older
release alone: it describes that release. Do not change any files.
