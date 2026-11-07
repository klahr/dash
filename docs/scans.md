# Scans

The Scans section runs preconfigured, read-only reviews of a project and adds what they find to
its Issues. Run one with "Run now", or let it run on a schedule. A scan uses its own throwaway
checkout of the default branch. It never makes a branch, so nothing goes to review or the merge
queue.

Scans come from three places, marked on each card:

- **Built-in:** Dash's security scan, code review and dependency audit, used when the repository
  has no scan files of its own.
- **Repository:** markdown files in `.dash/scans/<name>.md`, versioned and shared with your team.
- **Dash:** scans you write in the Scans section with "New scan", kept by Dash for this project
  only. "Duplicate" on any scan starts a new Dash scan from its prompt and settings, and
  "Export to repo" on a Dash scan shows its `.dash/scans/<name>.md` file to copy and commit.

A repository file:

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

- **Dash scans.** The editor has the same settings as a file's frontmatter (title, kind,
  scope, path, model, effort, schedule, tools) and the prompt, and checks them with the same
  rules. Only Dash scans can be edited or deleted in Dash; a scan's name can't change (duplicate
  it instead). Deleting one keeps its earlier runs and the issues it filed. Repository and
  built-in scans win a name clash: a Dash scan with the same name is shown as shadowed and does
  not run until you delete it or the other scan goes away.
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
  until you approve the new one. Built-in scans need no approval, and neither do Dash scans: only
  a Dash user can change them, so their tools and schedule apply as written.
- **Schedules.** The built-in scans' schedules are suggestions and a repository template's needs
  approval, so they only run once you turn them on; turning on a repository template's approves
  it. A Dash scan's schedule runs as soon as you save it. You can turn any schedule on or off, or
  change it, in the Scans section. A run missed while Dash was down runs once at
  startup, and a scan never runs twice at the same time. Scheduled scans wait while the
  subscription usage pauses the task queue. `-scan-schedules=false` turns schedules off.
- **Reports.** Each run shows what it found, e.g. "2 new, 3 already known, 1 reopened". The
  numbers link to those issues. A scheduled run that finds new critical or high issues sends a
  notification.

## API

- `GET /api/projects/{pid}/scans` lists the scans, each with `source` (`builtin`, `repo` or
  `dash`); a shadowed Dash scan has `shadowed_by` (the repository file, or `built-in`).
- `POST /api/projects/{pid}/scans` creates a Dash scan: `{"name", "title", "kind", "scope",
  "path", "model", "effort", "schedule", "tools": [], "prompt"}`. 409 if the name is taken.
- `PUT /api/projects/{pid}/scans/{name}` replaces a Dash scan's prompt and settings.
- `DELETE /api/projects/{pid}/scans/{name}` deletes a Dash scan; its runs and issues stay.
- `PATCH /api/projects/{pid}/scans/{name}` changes any scan's schedule and approvals, and
  `POST …/scans/{name}/run` runs it.
