# Scans

Scans are preconfigured, read-only reviews of a project. Each problem a scan finds
becomes a draft task on the project's Board. Run one with "Run now", or let it run on a schedule. A scan uses its own throwaway
checkout of the default branch. It never makes a branch, so nothing goes to review or the merge
queue.

Scans add their findings to the **Board**:

- **From scans (N)** above the board shows only the tasks scans added. Click it again to show
  every task. It shows once scans have added drafts.
- **⋮ → Scans…** in the top bar opens the **Scans** sheet: each scan, **Run now**, its schedule
  and settings, and **Create custom scan…**. `gn` opens it too.

Scans come from three places, marked on each card:

- **Built-in:** Dash's security scan (`security`), secrets scan (`secrets`), code review (`code-review`), dependency
  audit (`dependencies`), test gaps (`test-gaps`: changed code with no test covering it, and
  tests that can't fail), docs drift (`docs-drift`: README, `docs/`, CLAUDE.md, AGENTS.md,
  CONTRIBUTING, the CHANGELOG, OpenAPI and Swagger specs, `--help` text and `.env.example`
  content the code no longer matches), performance review (`performance`), dead code scan (`dead-code`),
  duplication and complexity (`maintainability`), flaky tests (`flaky-tests`), accessibility (`accessibility`: missing labels and alt text,
  poor colour contrast, keyboard traps, missing focus styles, wrong ARIA and non-semantic
  interactive elements) and breaking changes (`breaking-changes`: what the checkout's branch
  changes compared with the default branch that breaks callers or users: public API and HTTP
  endpoint changes, schema changes with no migration or a destructive one, removed or renamed
  flags, config keys and environment variables, and changed file formats), CI/CD and supply
  chain (`supply-chain`) and architecture digest (`architecture-digest`), in every project.
  Breaking changes has scope `since_main` and no suggested schedule: run it with "Run now" while
  the project's folder has the branch to check out. The security scan (suggested daily at
  06:00) traces untrusted input from where it enters to where it is used. It covers injection,
  authentication and authorization (including IDOR), CSRF, open redirects, CORS and security
  headers, XSS, file uploads, SSRF, unsafe deserialization, secrets, weak crypto and randomness,
  and denial of service an attacker can trigger cheaply: ReDoS, request bodies with no size
  limit, zip bombs and no rate limit on login. It looks at the changes since its last run, and
  at the whole repository again every 30 days (see **Full scans** below). The code review
  (suggested weekdays at 07:00) reports bugs with an input that triggers them: wrong logic,
  unhandled errors, nil and bounds, leaks, races and broken contracts. Both say what not to
  report and give a fingerprint format. The accessibility scan (suggested weekly, Wednesdays
  at 07:00) only runs in repositories with web front-end files (HTML, JSX, TSX, Vue, Svelte, Go
  and other HTML templates; see `requires_files` below) and is skipped in others. The
  performance review (suggested weekly, Mondays at 07:00) looks at what changed since its last
  run for N+1 queries, unbounded loops, reads and allocations, missing database indexes for
  added or changed queries, and slow or blocking work in request handlers. In a web front end it
  also looks at bundle size, needless re-renders, large unoptimised images and render-blocking
  scripts. It only reports
  problems with a plausible real cost, and each finding explains the cost and the fix. The
  duplication and complexity scan (suggested monthly, on the 15th at 06:00) looks at the whole
  repository for duplicated logic (naming every copy and where one shared version should live),
  functions too long, nested or branchy to follow, and needless layers: pass-through wrappers,
  single-implementation abstractions, unvaried options and hand-written versions of existing
  helpers. It skips style nitpicks, small repeats, generated and vendored code and migrations,
  and each finding says what gets simpler and how risky the change is. The
  dependency audit reports vulnerable, abandoned
  and far outdated dependencies. Each vulnerability names its advisory id (CVE, GHSA or OSV). One
  that no audit tool's output confirmed (`govulncheck`, `npm audit`, `pip-audit`, `cargo-audit`)
  is titled "Unverified:" and is at most `medium`. A missing audit tool is reported once, as
  `low`. It also checks licences against the project's own (its LICENSE file
  or manifest, else proprietary): dependencies whose licence clashes with it (GPL or AGPL in an
  MIT or proprietary project, say), with no or an unknown licence, or whose licence changed in an
  upgrade in the lock files' recent history. It reads the licences from the package managers'
  lock files and metadata and installs nothing. It has no general shell: it may run only
  `govulncheck`, `npm audit` and `npm audit --json`, `pip-audit --disable-pip …`,
  `cargo-audit audit [--json] --file Cargo.lock`, and `pip show` (see
  [Shell commands](#shell-commands)). It runs `cargo-audit` directly, never `cargo`: the
  repository's `rust-toolchain.toml` and `.cargo/config.toml` choose what `cargo` runs.
  The CI/CD and supply chain scan (suggested weekly, Thursdays at 06:00) looks at what changed
  in the CI configuration (GitHub Actions, GitLab CI, CircleCI, Jenkins, …), Dockerfiles and
  package manifests since its last run (everything on its first run); it is skipped when nothing
  changed, and ends early when none of these files did. It reports
  third-party actions pinned to a tag instead of a commit SHA, `pull_request_target` and
  `workflow_run` workflows that run a pull request's code, script injection through
  `${{ github.event.* }}` in `run:`, missing or too broad `GITHUB_TOKEN` permissions, secrets
  exposed to forks or logs, and self-hosted runners on public repositories; images that run as
  root, use `:latest` or unpinned base images, keep secrets in build arguments or layers, run
  `curl | sh`, `ADD` from a URL or have no `.dockerignore`; and lock files that are missing or
  not used in CI, dependencies from git URLs or open ranges, install scripts, likely
  typosquatted names and registries over plain HTTP. Each finding's fix is the change to make:
  the SHA to pin to, the `permissions:` block, the pinned image. Known vulnerabilities and
  licences are left to the dependency audit, and secrets in files and history to the secrets
  scan. It uses read-only tools and only runs in
  repositories with CI configuration, a Dockerfile or a package manifest.
  The dead code scan (suggested monthly) reports unused functions, types, flags, routes, config
  keys and files, and TODO/FIXME comments older than 90 days (dated with git blame) worth
  turning into tasks. It checks "unused" against the whole repository, including references by
  string, reflection and templates, and leaves out the exported API of libraries.
  The flaky-test scan (suggested weekly, Sundays at 03:00) finds the project's test command (CLAUDE.md, the Makefile,
  package.json, the CI configuration, go.mod, …), runs the suite 3 to 5 times, shuffled where the
  toolchain supports it (`go test -shuffle=on`, Jest `--randomize`, pytest-randomly), and reruns
  what failed on its own. It reports tests that sometimes pass and sometimes fail, with how often,
  the failure output and the likely cause (timing, ordering, shared state, network). Tests that
  fail on every run are reported too, as `high` "Failing test:" findings with their own
  fingerprint (`failing:<file>:<test>`), and so is a suite that doesn't build: the default branch
  is broken. Failures caused by this computer (a service not running, a missing tool) are left
  out. It stops adding runs at about 25 minutes in total and installs nothing. Each run has a
  time limit: `timeout` (or `gtimeout`) where it exists, else the test runner's own
  (`go test -timeout`), else a `perl` alarm, so it works on macOS too.
  It needs `Bash` and runs the repository's own test code, so it needs approval: see
  [Shell commands](#shell-commands). A scan with `Bash` may run the commands its prompt
  asks for (and the network they use) and write to a temp directory, but not change the
  checkout's files; a scan without it is read-only.
  The secrets scan (suggested daily at 05:00, scope `since_last_scan`) looks for API keys,
  tokens, private keys and passwords. Its first run reads the files and the whole git history,
  every branch and tag, deleted files included: a secret removed from the code is still leaked
  and must be rotated. Later runs read only the commits since the last run. It also reports
  committed `.env` and other credential files, `.gitignore` missing entries for them, and code
  or CI steps that log secrets or auth headers. It tells placeholders, documented example keys
  and test values apart from real ones. A finding never shows the secret: only the file, the
  line, the commit and a redacted prefix (`ghp_Ab12… (40 chars)`). A live-looking secret in the
  current code is critical; one only in history is high. Its fingerprint is the file, the kind
  of secret and the commit that added it, so a leak stays one task after it is removed. It
  runs with the read-only default tools: no shell and no network.
  The architecture digest (suggested weekly, Fridays at 15:00) helps you keep the big picture
  while agents write most of the code. It reads what was merged to the default branch since
  its last run (the last 7 days on its first run; skipped when nothing was) and writes a short
  digest, not a changelog: packages and modules added or removed, new dependencies, changed
  public APIs, routes, flags, config and data formats, new patterns or a pattern used in a new
  way, and code that grew a lot. Each item links the tasks that caused it. It ends with up to
  three **Worth a look** notes: design drift, the same thing built twice by different tasks, or
  a big change that got a quick review. If nothing notable changed, it says so in one line.
  See [Digests](#digests).
- **Repository:** markdown files in `.dash/scans/<name>.md`, versioned and shared with your team.
  Only a runner's own projects read them: on the hub, a project has the built-in scans and the
  Dash scans, since the hub has no checkout.
- **Dash:** scans you write in the **Scans** sheet with **Create custom scan…**, kept by Dash for this project
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
# name: default is the file name
name: security
# kind: see Kinds below; sets the default issue type
kind: security
# scope: repo | since_last_scan | since_main | path
scope: since_last_scan
# path: optional, limits the scan to a directory
path: internal
# model, effort: optional, claude --model and --effort
model: opus
effort: high
# schedule: optional cron expression (this computer's time zone)
schedule: 0 6 * * *
# tools: optional, needs approval in Dash; default: read-only tools and Dash's git diff/log/show/blame/grep tools
tools: Read, Grep, Glob
# requires_files: optional, only run if the repository has such a file
requires_files: *.html, src/**/*.{ts,tsx}
# full_scan_days: optional, for since_last_scan: scan everything again after this many days
full_scan_days: 30
# first_run_days: optional, for since_last_scan: the first run looks at the last this many days
first_run_days: 7
---
Look for vulnerabilities in {{path}} …
```

- **Required files.** `requires_files` limits a scan to repositories that have at least one file
  matching one of its globs, under `path` if it has one. Before Claude starts, the run looks
  through the commit it scans (not `.git`, `node_modules` or `vendor`); with no match the run is
  skipped ("No files matching …") and costs nothing. Globs match paths from the repository root:
  `*` stays within a directory, `?` is one character, `**` is any number of directories (`**/`
  also none) and `{a,b}` is either. A glob without `/` matches a file name in any directory, as in
  `.gitignore`: `*.vue` is `**/*.vue`. The setting is for template files (built-in and
  `.dash/scans/`); scans written in Dash don't have it, so duplicating a scan that has it drops it.
- **Kinds.** A scan's kind sets the type a finding gets when it names none. Each finding on a
  draft also records the kind of scan that filed it (`scan_kind` in the task's `finding`):

  | Kind               | Aliases                                                   | Default type |
  | ------------------ | --------------------------------------------------------- | ------------ |
  | `security`         | `sec`, `vuln`                                             | vuln         |
  | `dependencies`     | `deps`, `dep`, `dependency`                               | vuln         |
  | `code_review`      | `review`                                                  | bug          |
  | `tests`            | `test`, `testing`, `coverage`                             | issue        |
  | `docs`             | `doc`, `documentation`                                    | issue        |
  | `performance`      | `perf`                                                    | issue        |
  | `dead_code`        | `deadcode`, `unused`, `unused_code`                       | issue        |
  | `flaky_tests`      | `flaky`, `flaky_test`, `flakes`                           | bug          |
  | `accessibility`    | `a11y`                                                    | bug          |
  | `breaking_changes` | `breaking`, `breaking_change`, `breaks`                   | bug          |
  | `maintainability`  | `duplication`, `duplicate_code`, `complexity`, `refactor` | issue        |
  | `supply_chain`     | `supplychain`, `ci`, `ci_cd`, `cicd`                      | vuln         |
  | `digest`           | `architecture`, `what_changed`, `changes`                 | bug          |
  | `custom` (default) |                                                           | issue        |

  Kinds are case-insensitive and `-` or a space works for `_` (`dead-code`, `Flaky Tests`); the
  canonical name is what is saved and exported.
- **Dash scans.** The editor has the same settings as a file's frontmatter (title, kind,
  scope, path, model, effort, schedule, tools) and the prompt, and checks them with the same
  rules. Only Dash scans can be edited or deleted in Dash; a scan's name can't change (duplicate
  it instead). Deleting one keeps its earlier runs and the drafts it filed. Creating one with
  the name of a built-in or repository scan replaces that scan; its schedule and approvals don't
  carry over. Deleting it brings the replaced scan back, with its schedule off.
- **Scopes.** `since_last_scan` looks only at what changed since this scan's last run, which
  makes it the cheap daily option. If nothing changed, the run is skipped. `since_main` looks at
  the checkout's current branch compared with the default branch. `{{path}}` and `{{since}}` in
  the prompt are filled in.
- **Full scans.** A `since_last_scan` scan only looks at old code on its first run. To look at
  everything again:
  - Set `full_scan_days` in the template. When the scan's last full pass is older than that many
    days, the next run scans the whole repository, even if nothing changed. The built-in
    security scan has `full_scan_days: 30`. The setting is for template files; scans written in
    Dash don't have it.
  - Click **Full scan** in a scan's details in the **Scans** sheet to run one full pass now.

  The run's note says why it scanned everything ("Periodic full scan (every 30 days)", "Full
  scan, asked for"). A failed full pass is tried again on the next run.
- **First run.** A `since_last_scan` scan looks at everything on its first run. With
  `first_run_days` it looks at what the default branch got in the last that many days instead,
  and is skipped when it got nothing. The architecture digest has `first_run_days: 7`. The
  setting is for template files.
- **Findings.** The scan answers with JSON findings (title, type, severity, file and line,
  details, fix and a fingerprint). Invalid findings are dropped and noted in the run's log. Each
  valid one becomes a draft task, as described in [Findings on the Board](#findings-on-the-board).
  The scan is shown the findings it reported before that a task still has, so it reuses their
  fingerprints.
- **Approvals.** Anyone who can merge to the repository can change its templates, so a
  repository template's `tools:` and `schedule:` only apply once you approve them in the **Scans**
  sheet. Until then the scan runs with read-only tools and its schedule is off. The approval
  covers the exact tool list and expression: if the template's tools change, it goes back to
  read-only until you approve again; if its schedule changes, the approved one keeps running
  until you approve the new one. Built-in scans need no approval, except `flaky-tests` (see
  [Shell commands](#shell-commands)), and neither do Dash scans: only a Dash user can change
  them, so their tools and schedule apply as written.
- **Schedules.** The built-in scans' schedules are suggestions and a repository template's needs
  approval, so they only run once you turn them on; turning on a repository template's approves
  it. A Dash scan's schedule runs as soon as you save it. You can turn any schedule on or off, or
  change it, in the **Scans** sheet. A run missed while Dash was down runs once at
  startup, and a scan never runs twice at the same time. Scheduled scans wait while the
  subscription usage pauses the task queue. `-scan-schedules=false` turns schedules off.
- **Run early if there's spare usage.** Turn it on under a scan's schedule to spend Claude usage
  that would otherwise go unused. The scan still runs once per cron period (from one cron time to
  the next). It starts early when your 5-hour usage window resets within the hour and less than
  70% of it is used. Otherwise it runs at the cron time, as before. A scan that already started
  in the period (early, scheduled or by hand) skips the cron time, and it doesn't start early
  less than half a period after its last run. It needs a cron schedule, and it waits for the same
  things a scheduled run waits for.
  - **When:** **Any time**, or **At night only**: the window must reset between 22:00 and 07:00,
    in this computer's time zone, as cron uses. The window only resets at night if someone used
    Claude in the evening, so on other nights the scan runs at the cron time.
  - The lead time (1 hour), the ceiling (70%) and the night range are fixed. The API takes
    `lead_minutes` and `max_percent` to change the first two for one scan.
  - On a hub, it weighs the usage of the Claude account of each runner that could take the scan,
    and sends the scan to a runner on an account with spare usage.
  - A run started early says **started early (spare usage)** in the scan's runs.
- **On a hub.** The hub runs no Claude (see [several computers](several-computers.md#claude-runs-on-each-users-own-runner)): it
  sends each run's Claude part to one of the project's runners of the member the run is for (who
  pressed **Run**, or who last turned its schedule on or changed it), or to a shared runner (see
  [Whose work a runner does](several-computers.md#whose-work-a-runner-does)): it checks origin's default branch
  out in its clone and runs Claude there, and the hub records the findings. While none of the
  project's runners is online the run waits, showing "no runner online", and goes ahead as soon as
  one connects; a project no runner has the labels of can't be scanned. `-scan-timeout` is the hub's.
- **Reports.** Each run shows what it found, e.g. "2 added, 3 already there". A
  red badge counts the critical and high drafts the run added, e.g. "1 critical · 2 high". A
  scheduled run that adds critical or high drafts sends a notification.

## Digests

A scan of kind `digest`, such as the built-in architecture digest, writes a digest instead of a
list of problems:

- **In the Scans sheet.** Open **Details** on the scan: its last run shows the digest, with links
  to the tasks it names. Earlier runs keep theirs under **Earlier runs**.
- **In your summary.** When your daily or weekly summary (**Account → Notifications**, see
  [notifications](notifications.md#summary)) covers the project, it lists the digest's headline
  items, each with a link to the full text. They come with the project; there is no box to tick.
- **Bugs.** Its notes are not draft tasks. Only something that is clearly a bug becomes a draft,
  as with other scans.

To write your own, give a repository or Dash scan `kind: digest`. Dash adds the answer format:
the digest's text in Markdown, up to five headline items, and findings for clear bugs only.

## Shell commands

Scans run unattended, with network, as the runner's user. A shell there could read your SSH
keys, `~/.claude.json` or the runner's token, so Dash limits it:

- **Named commands only.** A scan's `Bash` rules name the commands it may run, e.g.
  `Bash(govulncheck:*)`. Any other command is refused. The `dependencies` scan has only the audit
  and metadata commands listed above.
- **`flaky-tests` needs approval.** It runs any command, including the repository's own tests.
  A test merged to the default branch, or a prompt injection, could steal credentials. So it
  runs only once you approve it for the project: **Approve tools** on the scan in the **Scans**
  sheet, which says why. Until then its runs are skipped with a note. A runner started with
  `-claude-sandbox` (see [sandbox](sandbox.md)) runs it without approval. On a hub, a run sent to
  a runner without a sandbox is skipped until you approve it.
- **Claude Code's sandbox.** Every scan that may run commands runs them in Claude Code's own
  sandbox. Commands can't read the credentials the file tools can't: `~/.ssh`, `~/.gnupg`,
  `~/.aws`, `~/.config/gh`, `~/.config/gcloud`, `~/.claude`, `~/.claude.json`, `~/.netrc`,
  `~/.git-credentials`, `~/.docker`, `~/.kube`, `~/.npmrc`, `~/.pypirc`, cargo's and gem's
  credentials, and Dash's data folders (the runner's token, the database, logs and worktrees).
  They may write only the checkout, the temp folder, the audit tools' advisory databases and a
  throwaway package cache for the run. They can't change the shared Go, npm or pip caches your
  tasks build from. They reach only the package registries and advisory databases the audit
  tools use.

Where the credential deny list can't apply:

- Claude Code's sandbox needs macOS, or Linux with bubblewrap and socat. Where it can't start
  (Linux without them, Windows), Claude Code warns in the run's log and runs the commands without
  it. The `dependencies` scan is still limited to its named commands, but those commands can read
  any file you can. Install bubblewrap, or use `-claude-sandbox`, to close that gap.
- A private registry that needs `~/.npmrc` or `~/.pypirc` can't be reached from the sandbox. The
  audit then fails and the scan judges the versions from what it knows.
- A repository template you approve with plain `Bash` runs in the same sandbox, but it can still
  run any command there. Approve it only if you trust everyone who can merge.

## Findings on the Board

Every finding becomes a draft task on the Board. So does every problem Claude reports during a
task (`report_issue`, see [MCP](mcp.md)) and every issue sent to the API. The draft's prompt is
written from the finding (title, details, file and line, suggested fix, severity, type, rule), so
you can queue it as it is.

A draft from a finding gets tags:

- its type: `bug`, `security` or `issue`;
- its severity: `critical`, `high`, `medium`, `low` or `info`;
- its source: `scan:<scan name>`, or `reported` for Claude's reports and issues sent by hand.

Before Dash adds a draft, it looks for the same problem by fingerprint. The fingerprint is the
type, the file and the rule (or the title, when there is no rule). The line is left out, so a
problem keeps its fingerprint when code above it moves.

- **A task has it already** (a draft, or a task that is queued, running, in Review, merging or
  failed): no new draft. The task counts the report and its card shows it with the severity,
  e.g. "high · 2 reports". The same source twice in a row counts once, so a daily scan doesn't
  inflate it. The line and details are updated. A draft's prompt is written again, unless someone edited it.
- **A merged task had it:** the problem came back. Dash adds a new draft that names the earlier
  task.
- **Its draft was deleted:** Dash keeps nothing of a deleted draft, so the next report adds a
  new draft.

Two reports of one problem at the same moment, or twice in one scan, add one draft.

Every scan rates its findings on one scale, given in the shared closing text of each scan's
prompt:

- `critical`: fix today. An exploitable vulnerability with severe impact that anyone who can
  reach the app can use, or data loss in normal use.
- `high`: fix soon. A vulnerability that needs an account or a victim's action, a bug that
  breaks a main feature, a crash on ordinary input, a broken build or tests failing on every run.
- `medium`: a real problem with limited reach, or a finding the scan could not confirm.
- `low`: worth doing, but nothing breaks.
- `info`: a note with nothing to fix.

Each built-in scan says how the scale applies to what it looks for. When in doubt it picks the
lower one.

To keep the Board usable when scans find a lot:

- Drafts from scans with severity `critical` or `high` show on the Board. Lower ones show only
  under **From scans (N)** above the board. Click it to see every task scans added.
- Click a tag on a card to show only tasks with that tag. Remove the filter with its chip.
- Pick two or more drafts (tick their boxes, or **Select** at the foot of *Drafts*) and click
  **Combine** to make them one draft. It keeps all their
  fingerprints, so later reports of any of them land on it, and adds up their counts.

## API

- `GET /api/projects/{pid}/scans` lists the scans, each with `source` (`builtin`, `repo` or
  `dash`); a repository scan replaced by a Dash scan of the same name is listed too, with
  `shadowed_by: "dash"`. `tools_approved` says whether it runs with its tools; a built-in that
  needs approval says why in `approval_note`. `{"approve_tools": [the scan's tools]}` to `PATCH`
  approves them.
- `POST /api/projects/{pid}/scans` creates a Dash scan: `{"name", "title", "kind", "scope",
  "path", "model", "effort", "schedule", "tools": [], "prompt"}`. 409 if another Dash scan has
  the name; a built-in or repository scan of the name is replaced.
- `PUT /api/projects/{pid}/scans/{name}` replaces a Dash scan's prompt and settings.
- `DELETE /api/projects/{pid}/scans/{name}` deletes a Dash scan; its runs and drafts stay.
- `GET /api/projects/{pid}/todos?tag=scan:security` lists the tasks with a tag.
- `POST /api/projects/{pid}/issues` reports an issue: `{"title", "type", "severity", "file",
  "line", "details", "fix", "rule", "source"}`. It adds a draft or bumps the task that has it,
  and answers `{"todo", "result"}`. `result` is `new`, `updated`, `known`
  or `reopened` (it came back after a merge).
- `POST /api/projects/{pid}/todos/merge` merges drafts: `{"ids": []}`. The first one is kept.
- `PATCH /api/projects/{pid}/scans/{name}` changes any scan's schedule and approvals, and
  `POST …/scans/{name}/run` runs it; send `{"full": true}` to make a `since_last_scan` scan look
  at everything once. `{"early": {"on": true, "when": "any" | "night",
  "lead_minutes": 0, "max_percent": 0}}` sets **Run early if there's spare usage**; fields left
  out keep their value, and `0` means the default. The scan's `schedule.early` shows it, and each
  run's `trigger` is `manual`, `schedule`, `early` or `resume`.

A run's `counts` are `new`, `updated` and `known` (already there), and `reopened`.
Its `new_tasks` and `reopened_tasks` are the ids of the drafts it added. A digest scan's run
has `digest`: its `headline` items and its Markdown `text`.

## Upgrading from Issues

Dash used to keep findings on a separate **Issues** page. On upgrade, open issues become drafts
with the same id, so old links still open them. Dismissed issues are dropped.
Issues that had a task attach their fingerprint to that task. Old `/p/<project>/issues` links
open the Board.
