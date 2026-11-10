---
name: docs-drift
title: Docs drift
kind: docs
scope: since_last_scan
# Suggested: Mondays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 1
---
Check that the documentation of the project in {{path}} still matches the
code. Read the README, the files under docs/ and any CLAUDE.md, and compare
what they say with what the code actually does.

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

Only report a contradiction you have confirmed in the code. Leave out
typos, wording and style, docs that are merely incomplete, and features
that are documented as planned. Do not change any files.
