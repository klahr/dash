# Scans

The Scans section runs preconfigured, read-only reviews of a project and adds what they find to
its Issues. Run one with "Run now", or let it run on a schedule. A scan uses its own throwaway
checkout of the default branch. It never makes a branch, so nothing goes to review or the merge
queue.

Scans are markdown files in the repository, `.dash/scans/<name>.md`, so they are versioned and
shared with your team. A repository without any gets Dash's built-in security scan, code review
and dependency audit.

```markdown
---
name: security            # default: the file name
kind: security            # security | code_review | dependencies | custom: the default issue type
scope: since_last_scan    # repo | since_last_scan | since_main | path
path: internal            # optional: limit the scan to a directory
model: opus               # optional: claude --model
effort: high              # optional: claude --effort
schedule: 0 6 * * *       # optional cron expression (this computer's time zone)
tools: Read, Grep, Glob   # optional, needs approval in Dash; default: read-only tools and Dash's git diff/log/show/blame tools
---
Look for vulnerabilities in {{path}} …
```

- **Scopes.** `since_last_scan` looks only at what changed since this scan's last run, which
  makes it the cheap daily option. If nothing changed, the run is skipped. `since_main` looks at
  the checkout's current branch compared with the default branch. `{{path}}` and `{{since}}` in
  the prompt are filled in.
- **Findings.** The scan answers with JSON findings (title, type, severity, file and line,
  details, fix and a fingerprint). Invalid findings are dropped and noted in the run's log. Each
  valid one is added to Issues with source `scan:<name>`, matched by fingerprint: a known issue
  is updated, a dismissed one stays dismissed, and a resolved one that comes back is reopened.
  The scan is shown the issues it reported before so it reuses their fingerprints.
- **Approvals.** Anyone who can merge to the repository can change its templates, so a
  repository template's `tools:` and `schedule:` only apply once you approve them in the Scans
  section. Until then the scan runs with read-only tools and its schedule is off. The approval
  covers the exact tool list and expression: if the template's tools change, it goes back to
  read-only until you approve again; if its schedule changes, the approved one keeps running
  until you approve the new one. Built-in scans need no approval.
- **Schedules.** Schedules only run once you turn them on: the built-in scans' are suggestions,
  and turning on a repository template's approves it. You can turn any schedule on or off, or
  change it, in the Scans section. A run missed while Dash was down runs once at
  startup, and a scan never runs twice at the same time. Scheduled scans wait while the
  subscription usage pauses the task queue. `-scan-schedules=false` turns schedules off.
- **Reports.** Each run shows what it found, e.g. "2 new, 3 already known, 1 reopened". The
  numbers link to those issues. A scheduled run that finds new critical or high issues sends a
  notification.
