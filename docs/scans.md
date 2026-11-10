# Scans

The Scans section runs preconfigured, read-only reviews of a project and adds what they find to
its Issues. Run one with "Run now", or let it run on a schedule. A scan uses its own throwaway
checkout of the default branch. It never makes a branch, so nothing goes to review or the merge
queue.

Scans come from three places, marked on each card:

- **Built-in:** Dash's security scan (`security`), code review (`code-review`), dependency
  audit (`dependencies`), test gaps (`test-gaps`: changed code with no test covering it, and
  tests that can't fail), docs drift (`docs-drift`: README, `docs/` and CLAUDE.md content the
  code no longer matches), performance review (`performance`), dead code scan (`dead-code`) and
  flaky tests (`flaky-tests`), in every project. The
  performance review (suggested weekly, Mondays at 07:00) looks at what changed since its last
  run for N+1 queries, unbounded loops, reads and allocations, missing database indexes for
  added or changed queries, and slow or blocking work in request handlers. It only reports
  problems with a plausible real cost, and each finding explains the cost and the fix. The
  dependency audit reports vulnerable, abandoned
  and far outdated dependencies, and checks licences against the project's own (its LICENSE file
  or manifest, else proprietary): dependencies whose licence clashes with it (GPL or AGPL in an
  MIT or proprietary project, say), with no or an unknown licence, or whose licence changed in an
  upgrade in the lock files' recent history. It reads the licences from the package managers'
  lock files and metadata already on the computer and installs nothing.
  The dead code scan (suggested monthly) reports unused functions, types, flags, routes, config
  keys and files, and TODO/FIXME comments older than 90 days (dated with git blame) worth
  turning into issues. It checks "unused" against the whole repository, including references by
  string, reflection and templates, and leaves out the exported API of libraries.
  The flaky-test scan (suggested weekly, Sundays at 03:00) finds the project's test command (CLAUDE.md, the Makefile,
  package.json, the CI configuration, go.mod, …), runs the suite 3 to 5 times, shuffled where the
  toolchain supports it (`go test -shuffle=on`, Jest `--randomize`, pytest-randomly), and reruns
  what failed on its own. It reports tests that sometimes pass and sometimes fail, with how often,
  the failure output and the likely cause (timing, ordering, shared state, network). It stops
  adding runs at about 25 minutes in total, wraps each run in `timeout`, and installs nothing.
  Like the dependency audit it needs `Bash`.
- **Repository:** markdown files in `.dash/scans/<name>.md`, versioned and shared with your team.
- **Dash:** scans you write in the Scans section with "New scan", kept by Dash for this project
  only. "Duplicate" on any scan starts a new Dash scan from its prompt and settings, and
  "Export to repo" on a Dash scan shows its `.dash/scans/<name>.md` file to copy and commit.

A scan's name is its identity, and one scan uses each name: a Dash scan replaces a repository
or built-in scan of the same name, and a repository file replaces the built-in of the same name.
So `.dash/scans/security.md` is your team's own security scan instead of Dash's, while the other
built-ins keep running next to it. A repository file replaced by a Dash scan is shown as
"Replaced" and does not run until you delete the Dash scan. Two repository files with the same
name are reported as a problem; the second one is not used.

A repository file:

```markdown
---
name: security            # default: the file name
kind: security            # see Kinds below; sets the default issue type
scope: since_last_scan    # repo | since_last_scan | since_main | path
path: internal            # optional: limit the scan to a directory
model: opus               # optional: claude --model
effort: high              # optional: claude --effort
schedule: 0 6 * * *       # optional cron expression (this computer's time zone)
tools: Read, Grep, Glob   # optional, needs approval in Dash; default: read-only tools and Dash's git diff/log/show/blame tools
---
Look for vulnerabilities in {{path}} …
```

- **Kinds.** A scan's kind sets the type a finding gets when it names none, and every issue a
  scan files records its kind (`scan_kind`). Issues has a scan-kind filter once a scan has filed
  some, and the API and `list_issues` filter by kind too (see API below):

  | Kind               | Aliases                                     | Default type |
  | ------------------ | ------------------------------------------- | ------------ |
  | `security`         | `sec`, `vuln`                               | vuln         |
  | `dependencies`     | `deps`, `dep`, `dependency`                 | vuln         |
  | `code_review`      | `review`                                    | bug          |
  | `tests`            | `test`, `testing`, `coverage`               | issue        |
  | `docs`             | `doc`, `documentation`                      | issue        |
  | `performance`      | `perf`                                      | issue        |
  | `dead_code`        | `deadcode`, `unused`, `unused_code`         | issue        |
  | `flaky_tests`      | `flaky`, `flaky_test`, `flakes`             | bug          |
  | `accessibility`    | `a11y`                                      | bug          |
  | `breaking_changes` | `breaking`, `breaking_change`, `breaks`     | bug          |
  | `custom` (default) |                                             | issue        |

  Kinds are case-insensitive and `-` or a space works for `_` (`dead-code`, `Flaky Tests`); the
  canonical name is what is saved and exported.
- **Dash scans.** The editor has the same settings as a file's frontmatter (title, kind,
  scope, path, model, effort, schedule, tools) and the prompt, and checks them with the same
  rules. Only Dash scans can be edited or deleted in Dash; a scan's name can't change (duplicate
  it instead). Deleting one keeps its earlier runs and the issues it filed. Creating one with
  the name of a built-in or repository scan replaces that scan; its schedule and approvals don't
  carry over. Deleting it brings the replaced scan back, with its schedule off.
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
- **On a hub.** The hub runs no Claude (see [several computers](several-computers.md#claude-runs-on-each-users-own-runner)): it
  sends each run's Claude part to one of the project's runners: it checks origin's default branch
  out in its clone and runs Claude there, and the hub records the findings. While none of the
  project's runners is online the run waits, showing "no runner online", and goes ahead as soon as
  one connects; a project with no runner linked can't be scanned. `-scan-timeout` is the hub's.
- **Reports.** Each run shows what it found, e.g. "2 new, 3 already known, 1 reopened". The
  numbers link to those issues. A scheduled run that finds new critical or high issues sends a
  notification.

## API

- `GET /api/projects/{pid}/scans` lists the scans, each with `source` (`builtin`, `repo` or
  `dash`); a repository scan replaced by a Dash scan of the same name is listed too, with
  `shadowed_by: "dash"`.
- `POST /api/projects/{pid}/scans` creates a Dash scan: `{"name", "title", "kind", "scope",
  "path", "model", "effort", "schedule", "tools": [], "prompt"}`. 409 if another Dash scan has
  the name; a built-in or repository scan of the name is replaced.
- `PUT /api/projects/{pid}/scans/{name}` replaces a Dash scan's prompt and settings.
- `DELETE /api/projects/{pid}/scans/{name}` deletes a Dash scan; its runs and issues stay.
- `GET /api/projects/{pid}/issues?kind=<kind>&type=<type>` lists the issues a kind of scan filed
  (aliases work; an unknown kind is a 400), optionally of one type (`bug`, `vuln`, `issue`). The
  MCP `list_issues` tool takes the same `kind`.
- `PATCH /api/projects/{pid}/scans/{name}` changes any scan's schedule and approvals, and
  `POST …/scans/{name}/run` runs it.
