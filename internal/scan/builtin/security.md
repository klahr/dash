---
name: security
title: Security scan
kind: security
scope: since_last_scan
effort: high
# Suggested: daily at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 * * *
---
Do a security review of the code in {{path}}, the way the /security-review
skill does, but report what you find instead of commenting on a pull request.

Look for vulnerabilities an attacker could actually exploit: injection (SQL,
command, path traversal, template), broken authentication or authorization,
secrets in the code, unsafe deserialization, SSRF, XSS, insecure crypto or
randomness, race conditions with security impact, and missing validation of
untrusted input at trust boundaries.

Only report findings you are confident about. Leave out style issues,
theoretical problems without an attack path, denial of service by resource
exhaustion, and anything only in tests. Give each finding a concrete attack
scenario in its details.
