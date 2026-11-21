# Several computers

Run Dash on each computer (yours at home and at work, or each teammate's), each with its own
projects and Claude login, and watch them all from one dashboard. The Dash you open in the browser is the **hub**. The other
computers are **runners**: they connect out to the hub and keep that connection open, so they
need no open port, VPN or shared secret. The hub must be reachable over HTTPS from the runners,
see [Deploy the hub](deploy.md). Setting up a team? Start with [Set up a team](team.md).

**A runner only does its owner's work**: it runs Claude with its owner's login. Each person needs a
runner of their own, or a [shared runner](#shared-runners-use-an-api-key) with an API key for
tasks. Chats, `!` commands, terminals, **Split…** and titles always run on one of your own
runners (see [Whose work a runner does](#whose-work-a-runner-does)).

## Contents

- [Two kinds of projects](#two-kinds-of-projects): a runner's own, or shared on labels
- [Add a runner](#add-a-runner): install, update, manage, Windows, a planner's runner
- [Shared projects: tasks on runners](#shared-projects-tasks-on-runners)
  - [Runner labels](#runner-labels)
  - [Where deploys run](#where-deploys-run)
  - [Whose work a runner does](#whose-work-a-runner-does): shared runners, command approval
  - [A runner's projects](#a-runners-projects)
  - [Moving projects off the hub](#moving-projects-off-the-hub)
- [Claude runs on each user's own runner](#claude-runs-on-each-users-own-runner): chats, scans,
  titles, laptops that sleep, GitHub access, where clones live
- [Projects of a runner's own](#projects-of-a-runners-own): adding them, goals, the API
- [What a runner lets the hub do](#what-a-runner-lets-the-hub-do)
- [Disk space on a runner](#disk-space-on-a-runner)
- [When a runner goes offline](#when-a-runner-goes-offline)
- [Previews](#previews)

The hub is a coordinator and **runs nothing on its own computer**: it signs people in, keeps the
users, the board and the database, and forwards work to runners. Every task, scan, chat, git
command, `gh` call, project command (setup, test, run, deploy) and terminal runs on a runner, and
the hub keeps no working tree of any project. So **a hub needs at least one runner**, and needs
**no `claude`, `git` or `gh` installed**. There is no flag to make the hub run things itself.

## Two kinds of projects

Every project lives on runners, in one of two ways:

- **A runner's own project**: a repository on one runner's disk, in its `-projects` directory. Its
  tasks, git and commands run on that runner only, and it shows in the dashboard under the
  runner's name. Make one with **Add from this runner…** on the runner's page in
  **Runners**, or start a new one with **+ Add project** → **Local only**. See [below](#projects-of-a-runners-own).
- **A shared project**: the hub's project, a git remote that runs on runner labels. It never names
  runners: the org's runners that have every label it runs on work on it, as a GitHub Actions job's
  `runs-on` picks runners (see [Runner labels](#runner-labels)). Each of them keeps its
  own clone of the remote, in its organization's folder inside its projects folder
  (`<-projects>/<org>/<owner>/<repo>`), any of them can run its tasks, and work lands on the remote.
  It shows in the dashboard with a small "shared" mark (two people). Make one with **+ Add
  project** (see [Add project on the hub](#add-project-on-the-hub)). Change its labels in its
  **Project settings → Runners**, or a runner's labels on its page in Runners (see
  [A runner's projects](#a-runners-projects)). See [below](#shared-projects-tasks-on-runners).

A project that is only ever worked on from one computer is simplest as that runner's own; share
it when several people's runners (or several of yours) should take its tasks.

### Add project on the hub

The hub keeps no folders, so **+ Add project** never asks for one. The dialog lists where the
repository comes from down its side (across the top on a phone), and beside it only the fields for
that choice. With more than one runner you may add projects with, pick it beside the title (yours
are marked "(yours)"); with one, there is nothing to pick. The picked runner lists your GitHub
repositories and clones the new project. The project runs on the labels under **Advanced → Runs
on**: by default the picked runner's own labels, or `any` (any runner of the org) while it has
none.

- **Write a project brief** (at the top): start from an idea instead. Claude interviews you and
  writes a brief, then one of the sources below makes the repository. See
  [Write a project brief](plan-project.md).
- **From GitHub** (the default): the repositories of the GitHub account the runner's `gh` is signed in to,
  and of the organizations that account belongs to (one `gh repo list` per owner). Each row shows
  the GitHub mark, `owner/name` and its description, and whether it is private, a fork or
  archived and when it was last updated, under a heading per owner (with their GitHub avatar). The
  filter box matches names and descriptions. A repository that is already one of the org's
  projects is marked and can't be picked again.
- **New GitHub repository**: a name (or `owner/name` for an organization) creates an empty
  repository with the runner's `gh`, private unless you tick **Public**, and makes the project from it.
- **From URL**: any `https://`, `ssh://` or `git@host:path` remote. A local path or `file://` URL
  is refused: runners clone the remote, and another computer can't reach a path on this one.
- **Local only**: no remote and no GitHub, for throwaway work. The runner makes a new repository
  in a new folder named after the project in its projects folder (in the org's folder there),
  with `git init` and an initial commit, and the project is that runner's own (not shared). Only
  the runner's owner can make one, and a folder of that name that already holds something is
  refused, never taken over. Deleting the project deletes its folder on the runner too (the
  delete dialog says so). To put it on GitHub later, run `gh repo create --source . --push` in
  the folder and share it as a new project.

Listing and creating repositories use the runner owner's GitHub login, so only the runner's owner
can use those two (anyone can paste a URL); the hub has no GitHub login of its own. The **Name**
shows once there is a repository, filled in with the repository's (change it to show the project
under another name; Local only needs one). **Advanced** holds the [clone URL](#how-a-runner-reaches-github)
and the start server, test and deploy commands, and **Deploy runs on** (the deploy command becomes
the environment `production`, see [Where deploys run](#where-deploys-run)); the commands and
environments can be set later in Project settings too. The button says what it will do (**Add owner/repo**, **Create repository and add**, **Add
project**, **Create local project**) and stays disabled until the choice has what it needs.

Once you click it, the dialog shows the steps as they go: **Creating GitHub repository** (for a
new one), **Adding project**, then the runner **cloning** it, which the hub has it do at once
(as the Runners page does) so a remote it can't read says so right there. A step that fails
shows why, with **Try again** (from that step: a repository already created isn't created
twice) and **Change details**, back to the form as you left it; when only the clone failed, the
project is there and **Open project** opens it. A clone taking longer than about 45 seconds carries
on out of sight ("Clone started on …"), and an offline runner clones it when it is back. Done, the
dialog closes on the new project.

### The Projects list

The dashboard's **Projects** list is a project picker: each project is listed once, however many
runners work on it, by name. The org's shared projects come first, under **Shared**; a runner's
own (local-only) projects follow under **On <runner>**, one heading per runner, alphabetically.
With only one group (only shared projects, say) there are no headings at all. A project's place in
the list doesn't change when its runners come online, go offline, or gain or lose its labels.

The dot on a project's icon says Claude is working on it (yellow) or work waits for review (green).
A project gets a badge only when its runners need attention:

- **needs a runner**: no runner has the labels it runs on (give one those labels, or change them in
  Project settings → Runners);
- **runner offline** / **all runners offline**: none of its runners is online;
- **1/2 online** (and so on): some of its runners are offline.

Hovering the badge lists its runners and whether each is online. No badge means every runner that
works on it is online.

At the bottom of the list, **Runners** opens the Runners page (the same as account menu →
Runners). Its dot sums up the org's runners: green when all are online, amber when some are
offline, red when none is, grey when there are none yet.

## Add a runner

In the hub's dashboard, open the account menu → **Runners** → **Add a runner** (any member
can, in a signed-in browser). It shows a one-time code (valid for 10 minutes) inside a command to
paste on the other computer (Linux, macOS or [Windows](#windows-runners), amd64 or arm64). The code is bound to you and the
organization you're working in: the runner will be yours, there, and run tasks with your Claude
login.

```sh
curl -fsSL https://dash.example.com/runner/install.sh | sh -s -- --code ABCD-EFGH
```

That's all. The script, served by the hub (it holds no secrets, so it needs no sign-in):

1. downloads the [release](releases.md) of the hub's own version (the latest release if the hub
   is a build from source), checks it against the release's `checksums.txt`, and installs
   `dash` in `~/.local/bin` (`--prefix DIR` for another place). It never uses sudo;
2. checks that `git` is installed, that `claude` is on the PATH and logged in, and that the hub
   answers over HTTPS, and says how to fix whatever isn't (`--skip-checks` goes ahead anyway).
   The code is only used once the checks pass;
3. registers with the hub. The runner is named after its host; the dialog's **Name** field (or
   `--name work`) picks another, and **Projects directory** (`--projects ~/code`) sets where its
   projects live. It swaps the code for its own token, saved in
   `~/.local/share/dash-runner/runner.json` (readable only by you). A runner's data directory
   (`--data`) is `~/.local/share/dash-runner` and a hub's `~/.local/share/dash`, so a hub and a
   runner on one computer don't share a database; each refuses to start in the other's. (A runner
   registered in `~/.local/share/dash` before keeps using it, or, when a hub uses that directory
   too, moves its registration to `~/.local/share/dash-runner` and starts afresh there.)
   Registering again from the same computer replaces its old registration, name included;
4. installs and starts a background service: a systemd **user** unit (`dash-runner.service`) on
   Linux, a LaunchAgent (`com.github.klahr.dash-runner`) on macOS, a scheduled task (`Dash runner`)
   on Windows. It reconnects by itself,
   waiting longer between attempts while the hub is down (up to a minute). `--no-service` skips
   this and runs the runner in the terminal instead.

The dialog says **Connected ✓** when the runner comes online, then lists the git repositories it
finds in its projects directory to add as [its own projects](#projects-of-a-runners-own).

On Linux a user service stops when you log out unless lingering is on; the installer says so,
and an admin can turn it on with `sudo loginctl enable-linger <you>`.

### Update a runner

On the Runners page, an out-of-date runner has an **Update to …** button for its owner and the
organization's admins. The runner goes into maintenance mode: the hub sends it no new tasks or
scans (they wait, saying "*name* is updating", or go to the project's other runners), and the
tasks it is running finish. Its page shows "Waiting for N tasks to finish…" until then; the update
can be cancelled until it starts. Then the hub tells the runner to update: it downloads the hub's
release from GitHub, checks it against the release's `checksums.txt`, keeps its binary as
`dash.prev` and puts the new one in its place, and restarts on it with the same flags (`--data`
included):

- under its background service it exits with status 4, and systemd (`Restart=on-failure`),
  launchd (`KeepAlive`) or, on Windows, the scheduled task's supervisor starts the new binary;
- started by hand (installed with `--no-service`), it replaces its own process with the new
  binary (`exec`; on Windows it starts the new binary in the same window and waits for it), in
  the same terminal.

Maintenance mode is the hub's, so it survives the runner's reconnects and the hub's restarts. It
ends when the runner connects running the hub's version, or when the update fails: the runner can't
write its binary (installed by root into `/usr/local/bin`, say: update it the way it was
installed), the download or its checksum is wrong (the old binary stays), or the runner comes back
on its old version. The Runners page then shows why. The runner's own Pause is separate and stays
as it was.

Back on the new version, the runner keeps its place in its Claude account's weekly pace: while the
account waits for the pace line, the runner waits too. If the hub restarted as well and doesn't
know the runner's account yet, tasks wait, saying "*name* is starting up", until the runner has
checked which Claude account it is on.

A just-updated runner tries the new version: if it doesn't connect to the hub within two minutes,
or keeps crashing on start (three starts without connecting), it puts `dash.prev` back and
restarts on it, and the hub reports the update failed.

**Updates itself** (the runner's settings, off by default) starts the same update by itself
whenever the runner connects running another version than the hub's, e.g. after the hub is
upgraded. It doesn't retry a release whose update failed or was cancelled.

A hub built from source has no release to update to: there is no button, and nothing updates
itself. `dash runner update [--version X]` on the runner's computer does the same download, checks
and swap by hand (default: the hub's version), then restarts the service.

The install command works too. Run it again **without** `--code`:

```sh
curl -fsSL https://dash.example.com/runner/install.sh | sh
```

It installs the hub's version if it differs, keeps the registration (and projects directory),
and restarts the service. A runner set up with `--data` needs the same `--data` again. The **Runners** sheet shows each runner's version and flags those that
differ from the hub's, with this command to copy.

### Managing the runner

```sh
dash runner status      # registration, hub, connection, service
dash runner install     # (re)install and start the background service
dash runner uninstall   # stop and remove it; the registration is kept
dash runner update      # install the hub's version (or --version X) and restart the service
```

Environment variables for the runner (`ANTHROPIC_API_KEY` for a [shared
runner](#shared-runners-use-an-api-key), `DASH_BASH`, …) go in `runner.env` in its data directory,
one `KEY=value` per line. The runner reads it each time it starts, so it reaches the background
service too; `dash runner install` and updates leave it alone. Run `dash runner install` after
changing it.

Logs: `journalctl --user -u dash-runner` on Linux, `~/Library/Logs/dash-runner.log` on macOS,
`%LOCALAPPDATA%\dash-runner\dash-runner.log` on Windows.

### Windows runners

A runner runs on Windows 10 or later (and Windows Server 2016 or later), amd64 or arm64. It
needs [Git for Windows](https://gitforwindows.org) (`winget install Git.Git`), which Claude Code
on Windows needs too: the runner runs project commands (setup, test, run, deploy) and a chat's
`!` commands with its `bash.exe`, found next to `git` on the PATH (`DASH_BASH` names another). The dialog shows a
PowerShell command next to the one for Linux and macOS:

```powershell
& ([scriptblock]::Create((irm https://dash.example.com/runner/install.ps1))) -Code ABCD-EFGH
```

It does what `install.sh` does, with PowerShell parameters (`-Name`, `-Projects`, `-Data`,
`-Prefix`, `-NoService`, `-SkipChecks`), and needs no administrator rights:

- `dash.exe` goes to `%LOCALAPPDATA%\Programs\dash`, which is added to your PATH;
- the checks include bash;
- the background service is a scheduled task, `Dash runner`, that starts the runner when you log
  in, with its log in `%LOCALAPPDATA%\dash-runner\dash-runner.log` (`-log-file`). The task
  starts a small supervisor, which starts the runner with no console window (so Claude Code, git
  and bash open none either) and starts it again whenever it stops: at once after an update,
  otherwise after 5 seconds, waiting longer (up to 5 minutes) while it keeps stopping. The log
  says why it stopped, with a crash's error. A window may flash at logon while the supervisor
  starts. It runs only while you are logged in. `dash runner install`, `uninstall` and `status`
  manage it as on the other systems.

It is a scheduled task, not a Windows service, so it needs no administrator rights: a service
needs them to install, and your password to run as you.

Run the command again without `-Code` to update. Windows won't overwrite or delete a running
program, so an update renames the running `dash.exe` to `dash.exe.old` and puts the new one in its
place. The supervisor keeps running the binary it started from until the scheduled task restarts
(at your next logon), so an update can't delete that binary's old `dash.exe.prev` or
`dash.exe.old` either: it renames them to `dash.exe.prev-<number>` or `dash.exe.old-<number>`.
Each update deletes the ones no longer running.

If an update still fails with "Access is denied" on `dash.exe.prev` (a runner older than this
fix), stop and start the runner's scheduled task, delete the leftovers, and update again:

```powershell
Stop-ScheduledTask -TaskName "Dash runner"; Start-ScheduledTask -TaskName "Dash runner"
Remove-Item "$env:LOCALAPPDATA\Programs\dash\dash.exe.prev*", "$env:LOCALAPPDATA\Programs\dash\dash.exe.old*"
dash runner update
```

Stopping a task's processes stops their whole process tree
(`taskkill /T`); there are no signals, so a stopped server gets no chance to shut down cleanly.

Not yet on Windows: **terminals** (and terminal previews) need a pseudo-terminal Dash doesn't
open on Windows yet; starting one says so. Everything else (tasks, chats, scans, servers and
previews over HTTP, tests and deploys) works as on the other systems.

### Manually

Without the script (or to build from source), the dialog's **Manual** tab has these steps:

1. Get `dash`: unpack a [release download](releases.md), or `go build -o bin/dash ./cmd/dash`
   in a checkout. It needs `git`, and `claude` logged in, like any Dash.
2. Register and run it:

   ```sh
   dash runner --server https://dash.example.com --code ABCD-EFGH
   ```

   Add `-name work` to pick a name. From then on just run `dash runner`.
3. Optionally, `dash runner install` keeps it running in the background as above (or write your
   own service around `dash runner`, see [Run as a service](service.md)).

A runner takes the same flags as `dash`, and its own: the flags about running work (`-projects`,
`-parallel`, `-claude`, `-claude-args`, `-codex`, `-codex-args`, `-claude-sandbox`, `-chat`, `-editor`, …) only matter to a runner; the hub
accepts them, logs a warning that it ignores them, and leaves them out of `dash -h`. It doesn't
listen on a port of its own unless you pass `-addr` (then it also needs `DASH_USERNAME` and
`DASH_PASSWORD`, like a normal Dash).

A runner belongs to whoever made its code, in the org they were working in: its token is one of
their [tokens](users.md#tokens), pinned to that org, and stops working if they leave it. The
**Runners** sheet lists the org's runners (other orgs never see them) as online or offline, with
when each was last seen, whose it is, its operating system (macOS, Linux or Windows, with its
logo), the Dash version it runs, how many tasks it is running and its owner's Claude usage. A
runner shows its operating system once it has connected with this version of Dash. It also shows the runner's Claude Code version. If Claude Code changes in a
way Dash relies on, the runner shows a warning, and so does the log of each task it runs. The runner
checks the version and whether `claude -p` still accepts `--output-format stream-json` and
`--permission-prompt-tool` when it starts and every hour. On every run, it checks that the
stream-json events (the session's start, messages, `rate_limit_event`, the result) still have the
fields Dash reads.

Its page in **Runners** also lists its **Agents**: each agent CLI it can run tasks with (Claude
Code, and any other agent this Dash supports), with its version, whether it is installed and
logged in, its warnings, and the features it has:

- **Resume**: it continues a task's earlier session (needed for **Pause** and **Build on**);
- **MCP**: it uses Dash's MCP servers (the task tools);
- **Permission prompts**: it asks you in Dash before a tool call it isn't allowed;
- **Usage**: it reports its account's usage limits.

The runner checks each agent when it starts and every hour; nothing needs setting up. A runner
older than this version reports only Claude Code, which the hub shows as its one agent.

Its owner and the org's admins can:

- **Pause** it: it finishes what it is running and gets no new tasks;
- set **At once**, how many of the hub's tasks it runs at a time (0: its own `-parallel`);
- **Revoke…** it (there, or under **Runners → Overview → Runners you registered**): it is disconnected at once, its token stops
  working (the hub keeps only a hash of it) and it works on no project any more. Tasks it was
  running fail and can be queued again on another runner. The runner exits, saying so (its
  systemd service doesn't restart it; launchd retries every 30 s); to bring it back, run the
  install command with a new code (revoking frees its name for that).

Runners registered with an older hub become the env account's.

### A planner's runner

A [planner](users.md#planners) can add a runner of their own, to use Claude with their own
Claude login: the general chat and **Write a feature brief**.

1. On the computer, install Claude Code and run `claude` once to sign in.
2. In Dash, open the account menu → **Runners** → **Add a runner**.
3. Paste the command it shows. It is the same install command members use.

A planner's runner runs only its owner's chat and feature brief interviews. The hub never gives
it the org's work: no tasks, goal coordinators, scans, tests, deploys, previews, shared work or
clones of projects, whatever its labels. It can't take other members' tasks. A planner sees,
renames and removes only their own runners. Members see it in **Runners**, marked as a
planner's.

## Shared projects: tasks on runners

The hub coordinates; the runners do the work. A shared project's tasks run on the org's runners
that have the labels it runs on ([Runner labels](#runner-labels)) and that do the work of the
member it is for: a runner runs only its owner's tasks, unless it is a shared runner with an API
key ([Whose work a runner does](#whose-work-a-runner-does)). From then on:

- Each task goes to one of the project's runners that is online, not paused, below its limit and
  not held back by its owner's Claude usage, the least busy first. A task that ran before goes
  back to the runner that has its worktree and Claude session. A task that builds on an earlier
  one (**Build on**) goes to the runner that ran the earlier one, when it can take it; on another
  runner it starts a fresh Claude session with the full task.
- To run a task on one runner only, pick it under **Runner** in the task form (or in the task's
  pane on a wide screen). The field shows when the project has more than one runner. The task
  then waits queued while that runner is offline or busy, and never goes to another one. Pick
  **Any** to let any runner take it again. You can change it while the task is a draft, queued
  or failed.
- If none can take it, the task stays queued and its card says why ("no runner online: laptop is
  offline", "waiting for a runner: laptop is busy (3 of 3 tasks)", "waiting: no runner of alice's
  has the labels Linux; laptop is bob's runner and only takes bob's tasks"). It starts as soon as
  one can.
- The project needs a git remote: each runner clones it into its projects folder the first time
  it works on it (see [Where a runner keeps a shared project](#where-a-runner-keeps-a-shared-project)),
  and keeps that clone. Work lands on the remote's default branch the same
  way it does without runners, but done by the runner. On GitHub that means a pull request (the
  default): when Claude finishes, the runner pushes the task's branch and opens one, and the task
  waits in the hub's Review column while it is reviewed on GitHub. The runner follows it: failing
  checks, conflicts and reviews asking for changes go back to Claude there. Once it is approved
  and its checks pass, a person merges it on GitHub: neither the runner nor the hub ever merges
  it (see [GitHub](github.md#pull-requests)). Deleting the task on the hub, or moving it back to drafts, closes its pull request. The hub
  marks the task merged once the runner reports the merge; the hub never needs a checkout, nor `gh`, of
  its own.
- Pick **Merge it** (or use a remote that isn't on GitHub) and the runner squashes the task onto
  the default branch in its clone (one commit), runs the project's tests and pushes it, without
  waiting for GitHub's checks. Runners don't need `gh` for this: a runner without it merges this
  way even when the project uses pull requests by default (not when you picked them).
- If origin moved meanwhile (another runner, or someone pushing), the push is rejected and the
  runner fetches, merges the task again on top of the new tip, re-runs the tests and pushes
  again. A real conflict or failing tests go back to Claude to fix, like any merge (within the
  project's automatic fixes), then the task returns to review or fails, saying why. **Revert**
  works the same way: the runner adds the revert commit and pushes it.
- Runners keep each other current. When a runner pushes the branch tasks land on (a merge, a
  revert, **Push**, a pull request it merged), it tells the hub, which tells the project's other
  online runners right away, over the connection they already keep open. Each one fetches and
  brings its clone's checkout up to origin; so does a runner before it starts a task there. Task
  worktrees are never touched, and running tasks stay on their runner. Runners fetch again
  whenever they reconnect, and every 5 minutes even when nobody looks at the project, so pushes
  made outside Dash (by hand, or a pull request merged on GitHub) show up too. A GitHub push
  webhook on the hub could trigger the same fan-out for outside pushes; it isn't there yet.
  - **A clone the runner made** (it cloned the project into its projects folder) is Dash's: nobody
    works in it, so it always follows origin, with no button to press. A clean checkout strictly
    behind fast-forwards. If something is in the way (uncommitted changes, untracked files,
    commits origin doesn't have), the runner saves it first on a new branch in that clone,
    `dash-backup/<date>-<time>` (e.g. `dash-backup/20261111-105738`; uncommitted changes are one
    commit on top of the local commits), logs the branch's name, and resets the checkout to
    origin's branch. Nothing is lost: `git branch --list 'dash-backup/*'` in the clone lists the
    backups, and they stay until you delete them. If the clone's config says `core.bare = true`
    although it has its files (the folder then fails every git command with "this operation
    must be run in a work tree"; a stray `git init` run with `GIT_DIR` pointing at one of its
    worktrees does that), the runner sets it back to `false`, logs a warning naming the clone, and
    syncs as usual. The sidebar shows such a clone only when syncing it fails for another reason
    (it can't write the files, say), as "beta: can't sync with origin" with the reason, and the
    backup branch if one was made; it retries on its own. A fetch that fails (offline, no access)
    shows on the project's origin pill as usual.
  - **An adopted clone** (it was already there, see below) may hold your own work, so it only
    ever fast-forwards: on that branch, clean and strictly behind. With uncommitted changes or
    commits of its own it is left alone; the project then shows "beta: 2 commits behind origin" in
    the sidebar, with a **Pull** button that fast-forwards only and says why when it can't. A
    webhook can be told when a checkout is left behind (`checkout.behind`, off unless you tick
    it; at most once every 6 hours per runner); a clone the runner made that fails to sync tells it
    too.
- Projects whose users never chose how work lands follow the default when the hub upgrades or
  their settings change: pull requests on GitHub, merging otherwise. A choice you made stays.
  Tasks with a pull request already open are followed until it is merged or closed, whatever the
  mode.
- Review happens on the hub as usual: the diff, the log and the timeline come from the runner,
  and **Run tests again** runs them on the task's runner, in its worktree there. Attachments and
  editor changes can't be sent to runners yet, and live previews are on the runner's own
  dashboard. A terminal in the task's worktree opens from the hub for the runner's owner (see
  below).

### Runner labels

Projects don't name the runners they run on. As on GitHub Actions, a project runs on **labels**,
and a runner works on every project of its org whose labels it all has. Labels say where a
project's work can go; whose tasks a runner then runs is the next section's rule:

- Every runner has the labels of its system (`Linux`, `macOS` or `Windows`) and its architecture
  (`X64`, `ARM64`, `ARM` or `X86`). A runner from before Dash reported its system has none.
- Every runner also has a label for each agent that is installed and logged in on it, named like
  the agent: `claude` for Claude Code, `codex` for Codex. A runner too old to report its agents
  keeps `claude`, as every runner ran Claude Code.
- Its owner or an org admin gives it labels of its own on its page in **Runners → Labels**. A
  label is letters, digits and `- _ . : + /`, up to 64 characters. `any` can't be a runner's
  label. Each change is saved at once.
- A project runs on the labels in its **Project settings → Runners → Runs on**. A runner works on
  it when it has every one of them, whatever their case: `any` is any runner of the org,
  `macOS` and `ARM64` is Apple silicon Macs, `gpu` is the runners you labelled so. A project with
  no labels runs on no runner, and says it **needs a runner**.
- A project also needs its agent's label. Pick the agent in **Project settings → Agent**; it is
  Claude Code unless you change it. Its tasks then go only to runners where that agent is
  installed and logged in. If no online runner has it, the setting says so, and the tasks wait.
  Actions the agent can't do are grayed out on its tasks, with a tooltip saying why. To use
  Codex, see [Codex](codex.md).

Every labels field works the same way. The picked labels show as chips; **×** on a chip removes
it. Below the field are the labels that already exist in the organization (on runners and
projects): click one to add it. To add a new label, type it and press `Enter` (or a comma).
`Backspace` in the empty field removes the last chip. Below the project fields, the runners that
match show as you pick, each with its online dot.

So adding a computer means giving it the right labels: it then takes its owner's work in every
project on them (and other members' work too, if it is a shared runner), with no project to
change. Taking a label off a runner, or changing a project's labels, makes
runners leave the project: each keeps its clone on its disk as a project of its own. Either change
is refused while one of the project's tasks is running on a runner it would leave (Claude working
on it, a review or a merge): "1 task of web is running on laptop: wait for them to finish (or
cancel them) before changing the labels". Tasks that finished running there (in review, failed, or
waiting to continue) keep their worktrees there; queued again, they start afresh on another of the
project's runners.

A runner clones a project it works on and does its git work (pushes, tags) with its owner's git
login. So a member can set a project's labels only so that it runs on their own runners or on
shared runners, besides those it already ran on; an admin can pick any runner of the org. When the
labels would also link someone else's runner, saving says so: "laptop is ana's runner and has the
labels gpu: only they or an admin of the organization can link projects to it". Give your own
runners a label of their own and use it. Labels never send your tasks to someone else's runner:
see the next section. A runner of another org never works on a project, whatever its labels.

A hub from before labels moves its projects to them when it starts: a project that was linked to
every runner of its org runs on `any`; any other gets a label of its own,
`project-<name>`, which the runners it was linked to are given. Each logs the move ("project now
runs on runner labels").

Runners used to have the label `self-hosted`. A hub from before `any` drops it when it starts: a
project that ran on `self-hosted` alone runs on `any`, one that ran on other labels as well keeps
only those, and a runner that was given `self-hosted` by hand loses it.

### Where deploys run

Each [environment](deploys.md) of a project deploys on any runner the project runs on, by default.
To keep an environment's deploys to some of them (the one with the signing keys, or the one in the
office network), give those runners a label of their own, such as `prod`, and pick it in the
environment's **Runs on** in **Project settings → Deploy**. A runner deploys there only if it has
every one of these labels, as well as the labels the project runs on. Below the field, the runners
that would deploy show as you pick, with a warning when none of them has the environment's secrets
file (`~/.dash/env/<environment>.env`, see [Secrets](deploys.md#secrets)).

- **Deploy** (on the **Deploys** page) goes to an online runner with those labels that works for
  you. If none has them, the deploy fails with "no runner of this project has the labels …
  deploys on"; if all of them are offline, it fails and names them. It never runs elsewhere.
- A runner without the labels refuses to deploy there even when asked: the hub holds that
  environment's commands back from it, as it does an [unapproved command](#project-commands-need-approval).
- Empty, or `any`, deploys on any runner the project runs on.
- Changing an environment, its labels included, counts as changing the deploy command: only those
  who may change the commands may change it (see [Project commands need approval](#project-commands-need-approval)).

The API field is `environments` on `PATCH /api/projects/{id}` and the `update_project` tool, each
with its `runs_on`, a list of labels (`[]` for none). A Dash that isn't a hub deploys on its own
computer and refuses labels.

### Whose work a runner does

A runner runs work on its owner's computer, as its owner, and Claude there is signed in with its
owner's login. So **a runner only does its owner's work**. The project stays shared: everyone in
the org sees it and adds tasks to it, and each member's tasks run on their own runners.

- A task runs on the runners of the member who created it. Labels pick which of their runners.
- A later run of a task is for whoever started it: queueing it, sending it back with feedback,
  retrying, continuing or resuming it, or changing what it gives Claude (its title, instructions,
  attachments, file edits or **Build on**). If that is someone else than
  the task's creator, the run needs a runner that does both members' work: a shared runner.
  Before you save such a change to another member's queued task, or queue their draft, Dash asks
  you to confirm. The task then waits: "needs a runner with the labels Linux that takes both
  alice's and bob's tasks (a shared runner)".
- A task Dash makes itself (a failing build's fix, a card from the project's tracker) is a
  member's, so their runners take it. The hub gives it, when it first looks for a runner for it,
  to the first of these who is still a member of the org (and not only a planner):
  1. for a tracker card: who connected the project's board to the tracker;
  2. for a failing build's fix: who turned on **Project settings → Approval & push → Fix failed
     builds automatically** (it is on by default, so often nobody did);
  3. who added the project;
  4. the org's longest owner, else its longest admin.

  The task still shows as made by Dash, and notifications say so. A task Dash made that a member
  then queues is theirs. Tasks Dash queued before this rule get an owner the same way.
- A task that is no one's needs a shared runner: one whose author's account was deleted, or one
  Dash made when none of those above is a member. A goal's
  tasks are the goal's owner's.
- Removing a member moves their queued tasks back to **Drafts**, as no runner would take them
  any more (see [Removing people](users.md#removing-people)).
- A scan run is for the member who pressed **Run**. A scheduled run is for the member who last
  turned its schedule on or changed it.
- **Run tests again**, and the project's start server, test and deploy commands and previews, run
  only on a runner of the member who starts them, or on a shared runner.
- **Runner** in the task form (and `run_on` in the API and board tools) only takes the task
  owner's runners and shared runners. A runner that can't take the task makes it wait, saying why
  ("laptop is bob's runner and only takes bob's tasks").
- Terminals, `!` commands and chats on a runner stay its owner's alone.
- The rest of a project's pages read the project's clone on one of its runners: **History**,
  **Files**, branches, releases. A read goes to a runner of yours first, then to a
  shared runner, and only when none of those is online to another member's runner. That runner
  clones and fetches with its owner's git login, so their login must reach the repository.
- Pushes, tags, switching or making the work branch, and a branch's pull request run
  only on a runner of yours or a shared one, never on another member's: they push
  with the runner's owner's git login. With none of those online they fail, saying which runners
  there are and why each doesn't take your work.

A task that waits because only other members' runners have its labels says so: "waiting: no runner
of alice's has the labels Linux; laptop is bob's runner and only takes bob's tasks". Its card shows
**Needs your runner**. Give one of your runners those labels, or ask for a shared runner. If alice
has no runner at all, it says that first ("alice has no runner: Runners → Add a runner, or ask an
admin for a shared one"), as labels on someone else's runner wouldn't help.

When no runner of yours (and no shared runner) has a project's labels, the board says so above
the lanes, with **Add a runner**. Admins see how many runners each member has in
**Organization → Members**: "no runner" means that member's tasks wait.

#### Shared runners use an API key

A Claude subscription (Pro, Max, Team or Enterprise) is one person's, and Team and Enterprise
seats are too: Dash never runs one member's work on another member's subscription. Only an
Anthropic API key, billed to the account that owns it (for example your company's Anthropic
Console account), can be shared. A **shared runner** runs Claude with an API key and has no
subscription login:

1. On the computer, use a user account with no Claude login. Check it without the key, and log
   out if it says `"loggedIn": true`:

   ```sh
   env -u ANTHROPIC_API_KEY claude auth status
   claude auth logout
   ```
2. Give the runner the key in `runner.env`, in its data directory. The runner reads this file
   each time it starts, under its background service too, and `dash runner install` and updates
   leave it alone. Variables already set in the runner's environment win.

   On Linux or macOS:

   ```sh
   umask 077
   mkdir -p ~/.local/share/dash-runner
   echo 'ANTHROPIC_API_KEY=sk-ant-...' >> ~/.local/share/dash-runner/runner.env
   dash runner install
   ```

   On Windows, in PowerShell:

   ```powershell
   $f = "$HOME\.local\share\dash-runner\runner.env"
   New-Item -ItemType Directory -Force (Split-Path $f) | Out-Null
   Add-Content $f 'ANTHROPIC_API_KEY=sk-ant-...'
   icacls $f /inheritance:r /grant:r "${env:USERNAME}:F" | Out-Null
   dash runner install
   ```

   A runner set up with `--data` reads `runner.env` in that directory instead. `dash runner
   install` restarts the runner so it reads the file; do that again after changing it.
   `dash runner status` shows the names the file sets (never their values). A runner started
   by hand can take the key on its command line instead: `ANTHROPIC_API_KEY=sk-ant-... dash runner`.
3. In **Runners**, open the runner, and under **Settings** turn on **Take other members' tasks**.
   Only its owner or an org admin can.

The runner's page says how Claude signs in on it ("API key", or which subscription) and whose tasks
it takes. **Take other members' tasks** can't be turned on for a runner that reports a
subscription login, or doesn't say how Claude signs in on it; the page says why next to it. When
that runner runs an older Dash than the hub, update it.

A shared runner also takes other members' tasks with labels it has. It runs them, and their
project commands, with the API key only: Dash removes `CLAUDE_CODE_OAUTH_TOKEN` and other login
variables from their environment, and gives Claude a config folder of its own
(`<-data>/shared-claude`), so it can't fall back to a login. When Claude starts, the runner checks
it signed in with `ANTHROPIC_API_KEY` and stops the run if not.

If a shared runner later reports a subscription login, it stops taking other members' work at
once: their running tasks stop there and wait for another runner, saying why, and their next
steps don't run there.

The runner checks all this too, not only the hub. Each start of work says whom it is for. A runner
refuses work for anyone but its owner unless **Take other members' tasks** is on and it signs in
with an API key alone. A runner registered with a hub too old to say who registered it treats all
work as other members': register it again with a new code.

#### Project commands need approval

A project's commands (setup, test, start server, terminal preview and deploy) run as the runner's
owner, on their computer, and a command could call `claude` itself. Dash records who last changed
each command. A runner runs a command only if an org admin set it, the runner's owner set it, it
was there before this rule, or the runner is a shared runner. Otherwise the step fails and says who
changed the command: an org admin or the runner's owner must approve it with **Approve commands**
in **Project settings → Commands**, shown while a command waits for them. An admin's approval counts
for every runner, an owner's for their own runners. The deploy command is all of the project's
[environments](deploys.md): changing one, or the labels it runs on, changes it.

#### Runners from before this rule

Runners used to work on every project whose labels they had, for every member. When a hub
upgrades, each runner registered before keeps doing so only if it is a shared runner by then:

- A runner that reports an API key and no subscription login, and that other members' tasks went
  to, gets **Take other members' tasks** on. The hub logs "runner: now takes other members' tasks".
- Any other runner (a subscription login, or a runner too old to say) takes only its owner's tasks.
  The hub logs "runner: now takes only its owner's tasks" with how it signs in. Other members'
  tasks that would have gone to it wait, saying why.
- Commands set before the upgrade count as approved.

The hub settles a runner when it first reports how Claude signs in; until then it takes only its
owner's tasks. Admins should check the hub's log for runners that stopped taking other members'
work. A team that wants shared runners needs one with an API key.

### A runner's projects

A runner's page in Runners shows its labels and the projects it builds. **Shared projects…** lists
the org's shared projects, each with the labels it runs on and whether the runner has them; it is
the same as each project's **Project settings → Runners**, from the runner's side, and a change on one
shows up on the other right away, in every open browser.

- **Add labels** on a project the runner doesn't build gives the runner the labels it lacks (for
  its owner or an admin). It then builds that project, and every other one on those labels.
- **For the runner's owner** it also lists the repositories of their GitHub login, as **+ Add
  project → From GitHub** does (through the runner's `gh`, a heading per owner, with a filter),
  then the org's other shared projects.
- **Create project** on a repository that isn't a project yet makes the shared project (named after
  the repository), on the runner's own labels (or `any` while it has none). If the runner is
  online and has them, it clones (or adopts) it at once, and a remote it can't read says so right
  away, in the words its tasks would wait with ("runner laptop cannot access owner/repo over ssh:
  git@github.com: Permission denied (publickey)"). The project stays either way. Such a project is
  the org's: everyone in the org sees it. For a repository only this computer works on, use **Add
  projects from this computer…** instead (see [below](#add-projects-from-a-runners-computer)).

Anyone in the org can open the screen to see what a runner builds. Only the runner's owner can list
GitHub repositories or create a project from one, because the list comes from their GitHub login
and the hub has none of its own. When an admin opens someone else's runner, the screen shows only
the org's existing shared projects and says why.

### Everything else about a shared project also runs on a runner

The hub has no clone of a shared project, so whatever needs one is forwarded, over the runner's
connection, to the clone of one of the project's runners that is online:

- the git views: **History** (the log and graph), branches and switching the working branch
  (made and pushed by the runner), the default branch's state against origin, **Push**, pull
  requests from the working branch, release tags (**Tag release…** and the `create_release` tool);
- the file browser and the `CLAUDE.md` and settings files;
- the project's commands: **Run** and **Test**, also in a task's worktree (they run on
  that task's runner), for any member of the org. The runner always takes the shared project's
  commands from the hub (set in its settings on the hub), each time it takes the project up. Only an
  org admin, or the owner of every runner the project runs on, can change those commands;
- the **Shell** view and **Open shell in this worktree** on a task: a terminal on the runner, in
  the project's checkout there or the task's worktree, only for the user who registered the runner
  (a shell can do whatever a deploy command can). The hub chooses neither the runner's folder nor
  whose terminal it is: the runner checks its owner itself. For the org's other members, or on a
  runner older than the hub, the Shell view says why.
  The header of the **Shell** view shows where the shell runs: the runner and its folder, such as
  `laptop · ~/…/dash`. When more than one of your runners with the project is online, the runner is
  a menu. Pick another runner to open a shell there; the shell you had keeps running, and picking
  its runner again shows it. The menu also lists the project's other runners, greyed out, with why
  not: **offline**, **not yours** or **update needed**. The browser remembers the runner you picked
  for each project. Until you pick one, or when yours is no longer online, the hub chooses one of
  your online runners with the project, one that opens terminals first;
- `!` commands in a project's or a task's chat: likewise only for the user who registered the
  runner, and only when its chat runs commands (`-chat-tools shell`; never with `-chat api`);
- GitHub Actions: the hub's **CI** view, the automatic investigation of a failing build, and
  **Retry** read and re-run the builds with `gh` on a runner;
- **Clean up leftovers** (**Project settings → Repository**, the palette or `:cleanup`) cleans the runner's clone.

When none of the project's runners is online these fail at once with "No runner is online for this
project. Start one with `dash runner`." (the dashboard also says so above the board); nothing falls
back to the hub. A project no runner has the labels of says it **needs a runner**, with a **Change
its labels** button.

The hub never runs tasks itself: a task of a project no runner has the labels of waits, saying
which labels it runs on. (`-hub-tasks` is gone; an old start command that still passes it starts, logs a
warning and ignores it.)

### Moving projects off the hub

A hub from before this kept projects of its own, in its own `-projects` directory. They are kept,
with their tasks and history on the board, and the hub deletes nothing on disk; at
startup it logs a warning for each project that runs on no runner labels (`project needs a runner`), and
the dashboard marks them **needs a runner**. Their work waits until you do one of these, per
project:

1. **Run it on runner labels** (Project settings → Runners → Runs on, e.g. `any`), if it
   has a git remote: the runners with them clone the remote and the project carries on as a shared
   project. A project without a remote can't run on runners: push it somewhere first (e.g. create the GitHub repository from the folder on disk with
   `gh repo create --source . --push`), then set up the project again as below.
2. **Remove it from the hub and add it on a runner**: delete the project in its settings (on the
   hub this only forgets it; its folder stays on disk), move or clone the repository into a
   runner's projects directory if it isn't on a runner's computer yet, then add it with **Add
   projects from this computer…** on the runner's row of the Runners page. It becomes that
   runner's own project.

Once no project is left on the hub's disk, the hub's `-projects` directory can go, and `git`, `gh`
and `claude` can be uninstalled from the hub's computer.

## Claude runs on each user's own runner

The hub doesn't run Claude at all, and can't be made to: it keeps the board (the database) and
serves the website, and the runners do all the Claude work, each with its own `claude` login (its
owner's subscription, or an API key on a shared runner). So **a hub needs at least one runner**: start `dash runner` on a computer
with git and Claude Code logged in (account menu → **Runners** → **Add a runner** gives the
command). Until one is online, the dashboard says so where Claude would be used. The hub needs
**no `claude` install, no `-claude` flag, no login and no API key**; it never starts `claude` and
never reads `ANTHROPIC_API_KEY`. Runners
keep no board of their own for the hub's work: what Claude changes on the board through Dash's
tools (problems it reports, tasks it creates) goes straight to the hub's.

Where each kind of Claude work runs:

- **Tasks**: on one of the project's runners that does the task's owner's work (see
  [above](#shared-projects-tasks-on-runners) and [Whose work a runner does](#whose-work-a-runner-does)).
  A task of a project no runner has the labels of waits, saying which labels it runs on.
- **Scans**: on one of the project's runners online of the member the run is for (or a shared
  runner), in a throwaway checkout of its clone at
  origin's default branch. The hub keeps the runs, their logs and the drafts they file; the run's
  log says which runner it ran on. With the project's runners all offline a run waits, showing
  "no runner online: laptop is offline", and starts when one comes back; a project no runner has the
  labels of can't be scanned. Repository scans (`.dash/scans/`) are used only by a runner's own
  projects; hub projects use built-in and Dash scans.
- **Project and task chats**: on one of your runners that works on the project (a task's chat on the
  runner that ran it). Shell commands typed in them with a leading `!` run there too, in the same
  folder, never on the hub: see [Chat](chat.md#shell-commands-with-).

- **Goals' coordinators**: a [goal](goals.md)'s wake-ups run on its owner's runner that works on the
  project, read-only in its clone; its chat on yours. With none online the goal's events wait,
  and its log says so. What the coordinator does with its tools happens on the hub's board.

- **General chat**: runs on one of your runners (a planner's too: see
  [A planner's runner](#a-planners-runner)), in a scratch folder (`<-data>/chat` on that
  computer), not in any project. The chat's header says which runner it runs on; with several
  online you pick one there, and with none online it says so. The hub keeps the conversation, as
  before. Files can't be attached to it yet.
- **Titles and summaries**: a task created without a title is named by Claude on one of your
  runners; with none online it is named after the first line of its instructions. A task's
  summary is written by the runner that ran it.
- **Clean up leftovers** and **Split…** (in the task form): run on one of your runners; with none online
  they say so.

Nothing falls back to running Claude on the hub: work with no runner to go to waits (tasks,
scans) or says so (chats, clean-up, split).

A runner does these only for its owner, the user whose code registered it: the hub sends a
user's calls only to that user's own runners in the org they're working in (never another
member's, admins included), and the runner checks the user the hub names against the one it was
registered by. Runners registered with a hub older than this can't check, and trust the hub; run
the install command with a new code to register them again.

Their Claude usage is the runner's: it shows with the runner's usage in **Runners**,
never as the hub's.

This used to be a choice (`-hub-claude`, with a single-user hub running Claude itself by default).
The flag is gone: an old start command that still passes it starts, logs a warning and ignores it.
Claude's flags (`-claude`, `-claude-args`, `-chat`, `-title-model`, …) are a runner's: the hub
ignores them, with a warning.

### Laptops that sleep

If a runner disconnects while running a task (the lid closes, the Wi-Fi drops), the hub waits
`-runner-grace` (default 2 minutes) for it to come back. After that the task fails, "interrupted:
laptop went offline mid-task", and its **Retry** button queues it again: the runner
picks up its worktree and Claude session where it stopped. If the runner kept going meanwhile (a
short outage), it says so when it reconnects and the task carries on. If the task reached review,
or was merged, before the hub sent it back, the board follows: a merged task shows as merged, with
its commit, and isn't sent again. A runner that can't reach
the hub for its `-offline-grace` stops its tasks itself (below).

### What a runner accepts from the hub

A runner takes work only for projects its hub linked to it, of the organization it was registered
for. It keeps the hub's id for the project with its checkout and finds it by that id, never by a
path from the hub: the hub sends a remote, and the runner picks the folder itself, inside its
`-projects` (it clones only `https://`, `ssh://` or `user@host:path` remotes, never a local path).
It refuses and logs anything else; the hub reaches only tasks it sent, by its own ids.

### How a runner reaches GitHub

By default (`-github-protocol=auto`) a runner clones a shared GitHub project over **SSH**
(`git@github.com:owner/repo.git`, with the computer's SSH keys), whatever form the hub's remote has.
When SSH can't read the repository (no key GitHub accepts, an unknown host key) and the GitHub CLI
is installed and logged in (`gh auth login`), it clones over **HTTPS**
(`https://github.com/owner/repo.git`) instead; when neither works the link fails with both reasons,
e.g. "SSH: Host key verification failed; HTTPS: gh isn't logged in (run `gh auth login`)".
`-github-protocol=ssh` or `-github-protocol=https` (or `DASH_GITHUB_PROTOCOL`) forces one, with no
fallback. The clone's origin is the URL it was cloned from, so fetches and pushes use the same
protocol. Clones made before keep their origin. A remote that isn't GitHub's is cloned as it is,
with the computer's own SSH keys or git credentials for that host.

Over HTTPS, Dash's own git commands (clone, fetch, push, in every worktree) get their credentials
from gh: they run with `gh auth git-credential` as a credential helper for `https://github.com`,
added through `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n` after any the environment
already has. Neither `gh auth setup-git` nor `~/.gitconfig` is needed or changed, and credential
helpers the user configured are still asked first.

A project can instead have a **clone URL** (Add project → Advanced → Clone URL, or `clone_url` in the API),
which every runner clones as it is: a remote that isn't GitHub's, or an SSH host alias for a deploy
key (`git@github-deploy:owner/repo.git`). It is a URL only; credentials in it are refused, and stay
on the runner.

Before cloning, the runner checks it can read the remote (`git ls-remote`, never prompting:
`GIT_TERMINAL_PROMPT=0`, ssh in batch mode) and otherwise fails the link with, for example,
"runner laptop cannot access owner/repo over ssh: git@github.com: Permission denied (publickey)".
An empty repository (just created on GitHub) passes; the runner gives it a first commit on the
project's default branch, whatever the computer's `init.defaultBranch`, and pushes it.

Creating a GitHub repository (Add project → New GitHub repository) still uses the runner's `gh` login,
whichever protocol it clones over.

### Where a runner keeps a shared project

When it starts working on a project, a runner puts it in the folder of the project's organization
inside its projects folder, in a folder for the remote's owner (the GitHub user or organization),
named after the remote: `https://github.com/klahr/dash.git` becomes `~/src/acme/klahr/dash` for a
runner started with `-projects ~/src`, in the organization whose folder is `acme`. A remote
without an owner (`ssh://host/dash.git`) goes straight in the organization's folder. That is the same folder name the hub uses for the organization's projects (the
organization's subdomain name when it was made; renaming the subdomain later doesn't change
it). A hub from before this sends no folder name; the runner then uses `org-<id>`
(`~/src/org-1/klahr/dash`). So the clones of different organizations never share a folder, nor share
one with the runner's own projects, and everything inside `<-projects>/<org>/` is Dash's.

- **A clone of the same repository is already in the owner's folder** (the remote
  compared without caring about `https` or `ssh`, credentials or a trailing `.git`): the runner
  adopts it instead of cloning. If it was one of the runner's own projects, that project becomes
  the shared one (leaving the project, when it loses its labels, makes it the runner's own again). A clone anywhere else, such as your
  own checkout at `<-projects>/dash`, is never adopted: the runner makes a clone of its own.
- **Something else is there** (another repository, a folder that isn't one, a file, a symlink, or
  a clone of the same remote already linked to another shared project): it is never touched; the
  runner takes the next free name in the owner's folder, `dash-2`, `dash-3`, … A repository or a
  file where the organization's or the owner's folder should be is in the way: linking says so,
  and works once it is moved.
- **Your own edits in an adopted clone**: tasks still work in worktrees, but landing one moves the
  checked-out branch of that folder. So while it has uncommitted changes to tracked files, a task
  you approved waits ("Waiting for you to commit or stash …") and lands once you commit or stash
  them; nothing is pushed meanwhile and your changes are never overwritten. Starting the project's
  run or test command there warns about them. Deploys never run in it: they run in a checkout of
  their own ([Where it runs](deploys.md#where-it-runs)). Switching the working branch only makes branches; it doesn't touch the folder.
- **Clones from before, straight in `<-projects>/`** or in `<-projects>/<org>/` without the
  owner's folder, move into the owner's folder on their own, with the release that brings this: on the runner's start, or at the hub's next link
  of the project (before each task it sends, and whenever the hub shows its files, branches or
  commands) if the runner didn't know the folder's name yet. The move renames the folder and runs
  `git worktree repair`, so the tasks' worktrees, task branches and uncommitted work come along;
  the hub then shows the new folder. Each move is logged, `runner: moved ~/src/dash →
  ~/src/acme/klahr/dash`. A project with a task running or merging, or a preview, try session or command
  running in it, moves later, once it is idle. If a move fails part way it is undone and the
  project keeps working from its old folder (the log says why). Only clones the runner made
  move: one it adopted may be your own checkout, and stays where it is.
- **Runners from before that** cloned shared projects into `<-projects>/hub/<name>`. Those stay
  where they are, with their worktrees, task branches and uncommitted work: on its first start
  after the upgrade the runner marks them as the hub's once and from then on finds them by id. To
  move one, take its labels off the runner, move the folder into `<-projects>/<org>/`, and give
  them back (the runner adopts it at its new place).

## Projects of a runner's own

A runner's own projects (those in its `-projects` that aren't linked from the hub) show in the
hub's dashboard for platform admins, under **On <runner>**, and their tasks, git and commands run
on that computer. Notifications come from all of them. The hub has no heading of its own in the
**Projects** list: its projects are the shared ones, under **Shared** (see
[The Projects list](#the-projects-list)). A runner's own projects can only be listed while it is
online.

### Add projects from a runner's computer

Right after a runner connects, the **Add a runner** dialog lists the git repositories in its
projects directory; later, **Add from this runner…** on the runner's page in **Runners** lists
them again. Each shows its folder name, its path in the projects directory,
its branch and its origin remote (without any credentials in the URL). Nothing is ticked: tick the
ones you want (or **Select all**) and **Add selected** adds them as the runner's own projects,
just like adding a project with that folder on the runner itself.

The runner does the looking, and only:

- in its projects directory: the folders there that are git repositories, and the ones a level
  down (`~/code/org/repo`), no deeper. Hidden folders, `hub/` (shared projects' clones from before
  they went straight into the projects folder) and repositories that are already projects (the
  shared ones among them) are left out. It never follows a symlink, so it never looks, or adds
  anything, outside the projects directory; a folder whose `.git` is a file or a symlink (a linked
  worktree, a submodule) isn't listed either;
- for its owner: the hub asks only for the user who registered the runner (not another member,
  not an org admin), and the runner checks that user as it does for [Claude](#claude-runs-on-each-users-own-runner);
- names, paths, branches and remotes: it reads `.git/HEAD` and `.git/config`, never the files in
  a repository. **Add selected** takes only paths that are in its listing at that moment.

**Goals** don't work on a runner's own project from the hub: the runner doesn't offer them, so
**New goal…** isn't shown. Use them in that computer's own Dash, or make the project a shared one.
See [Goals](goals.md).

### A runner's own projects through the API

Platform admins reach a runner's whole API under `/peer/{runner}/`: the hub forwards
`/peer/{runner}/api/…` to the runner's `/api/…`, for the routes in
[What a runner lets the hub do](#what-a-runner-lets-the-hub-do). `{runner}` is the runner's
name. For example, a project's settings:

```sh
curl -b cookies.txt https://dash.example.com/peer/team-laptop/api/projects/a1b2c3d4e5f6
```

Everyone else uses these routes on the hub, where `{id}` is the runner's id (from
`GET /api/runners`) and `{pid}` the project's id on the runner:

| Call | Who | What it does |
| --- | --- | --- |
| `GET /api/runners/{id}/repos` | its owner | the repositories in its projects directory |
| `POST /api/runners/{id}/repos` | its owner | add them as its own projects: `{"paths": [...]}`; with one path, `name` and `test_command` too |
| `POST /api/runners/{id}/projects` | its owner | a new local-only project: `{"name", "test_command", ...}` |
| `GET /api/runners/{id}/projects/{pid}` | anyone in its org | one of its own projects' settings |
| `PATCH /api/runners/{id}/projects/{pid}` | its owner | change them, with the body of `PATCH /api/projects/{id}` |

A shared project's clone on a runner isn't one of its own projects: change its settings on the
hub, with `PATCH /api/projects/{id}`.

```sh
curl -b cookies.txt -X POST https://dash.example.com/api/runners/r1b2c3/repos \
  -d '{"paths":["app"],"name":"App","test_command":"make test"}'
```

## What a runner lets the hub do

A runner only does the dashboard's normal work on its own projects: tasks, reviews, merges,
scans, chats about a project or task, CI, previews and permission prompts. It refuses:

- terminals, and any command other than the project's own **Run**, **Test** and **Deploy**, for
  anyone but its owner (it always takes the hub's project commands, and opens terminals and runs
  other commands for its owner). Stopping a terminal is its owner's too;
- adding a project outside its `-projects` directory;
- the general chat and Claude's short calls (titles, summaries, clean-up, split) for anyone but its owner
  (see [above](#claude-runs-on-each-users-own-runner));
- chats, and the shell commands typed in them with `!` ([Chat](chat.md#shell-commands-with-)), for
  anyone but its owner, and `!` commands at all when its chat is read-only (`-chat-tools readonly`);
- listing the repositories in its projects directory, or adding them, for anyone but its owner
  (see [above](#add-projects-from-a-runners-computer)).

Claude still runs tasks with the runner's `-claude-args` (and in its [sandbox](sandbox.md), if
it has one) and asks for permission as usual; the
runner reports the prompts of the hub's tasks to the hub, so they appear in the hub's dashboard
(and its notifications), and the hub sends your answer back to the runner. While the runner is
offline, its prompts aren't listed on the hub; they show again when it reconnects.

## Disk space on a runner

Every task builds and tests in its own worktree, and the build caches that fills can grow until
the disk is full. Go's cache (`~/.cache/go-build`) never trims itself on a schedule. Each runner
reports its free disk space to the hub with its heartbeat: the disk of its projects folder, and
those of its task worktrees and its cache folder if they are on other disks.

See it in **Runners → the runner → Disk**.

When a disk has less free than the smaller of 10% of its size and 10 GB:

- **Runners** marks the runner, and the board shows a banner to everyone in the organization
  until there is space again.
- The hub notifies once (push notifications and the `runner.disk` webhook event of the runner's
  projects), not on every heartbeat. It notifies again only after the runner had enough space,
  and at most every 6 hours per runner.
- The runner's page lists the five largest folders in its cache folder, with their sizes, so you
  can see what uses the space.

Below 2 GB free, the runner starts no task. The task waits, and its log says why, for example
"runner box has only 1.2 GB free".

The runner's owner and the organization's admins change these thresholds under **Disk**:
**Warn below** (percent and GB) and **Start no task below** (GB). Leave a field empty for the
default.

### Trim the Go build cache

**Trim the Go build cache above** (GB) is off by default. When you set it, the runner checks the
size of `go env GOCACHE` once a day and when space runs low, and runs `go clean -cache` when it is
bigger than the limit. It waits until no task, check or test command is running on the runner, so
it doesn't break a build. Dash deletes nothing else on its own.

To free space by hand, run this on the runner:

```sh
go clean -cache
```

Then check the other large folders the runner's page lists.

## When a runner goes offline

Its projects disappear from the dashboard until it's back; requests for it fail at once with
"offline". Tasks already running keep running on the runner. If the hub stays unreachable for
`-offline-grace` (default 10 minutes; 0 = never), the runner stops its running tasks and marks
them failed, "interrupted: this runner lost its connection to the hub", so none waits forever on a
permission prompt nobody can see. Their worktrees and Claude sessions are kept: queue them again
and they continue. A runner that is restarted resumes its tasks, as Dash always does.

## Previews

[Live previews](previews.md) run on the computer that has the project, and the hub serves them,
at `https://<task>-<runner>.<preview domain>/` (so only the hub needs `-preview-domain`, DNS and
a certificate). The runner keeps a second connection to the hub just for preview traffic, so a
heavy page load or a busy hot-reload socket doesn't slow its tasks down, and the hub opens at most
32 connections to a runner's previews at a time. The runner forwards only to the servers of
previews it started itself; the hub can't ask it for any other port.
