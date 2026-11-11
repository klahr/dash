# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. The Dash you open in the browser is the **hub**. The other
computers are **runners**: they connect out to the hub and keep that connection open, so they
need no open port, VPN or shared secret. The hub must be reachable over HTTPS from the runners,
see [Deploy the hub](deploy.md).

The hub is a coordinator and **runs nothing on its own computer**: it signs people in, keeps the
users, the board and the database, and forwards work to runners. Every task, scan, chat, git
command, `gh` call, project command (setup, test, run, deploy) and terminal runs on a runner, and
the hub keeps no working tree of any project. So **a hub needs at least one runner**, and needs
**no `claude`, `git` or `gh` installed**. There is no flag to make the hub run things itself.

## Two kinds of projects

Every project lives on runners, in one of two ways:

- **A runner's own project**: a repository on one runner's disk, in its `-projects` directory. Its
  tasks, git and commands run on that runner only, and it shows in the dashboard under the
  runner's name. Make one with **Add projects from this computer…** on the runner's row of the
  Runners page, or start a new one with **+ New project** → **Local only**. See [below](#projects-of-a-runners-own).
- **A shared project**: the hub's project, a git remote that one or more runners are linked to.
  Each linked runner keeps its own clone of the remote, straight in its projects folder
  (`<-projects>/<repo>`), any of them can run its tasks, and work lands on the remote. It shows in
  the dashboard under each runner it is linked to, with a small "shared" mark (two people). Make
  one with **+ New project** (see [New project on the hub](#new-project-on-the-hub)): it is linked
  to the runner picked as its **Runner**. Link more runners in its **Settings → Runners**, or pick
  the projects a runner builds from the runner's side, with **Projects…** on its row of the
  Runners page (see [A runner's projects](#a-runners-projects)).
  See [below](#shared-projects-tasks-on-runners).

A project that is only ever worked on from one computer is simplest as that runner's own; share
it when several people's runners (or several of yours) should take its tasks.

### New project on the hub

The hub keeps no folders, so **+ New project** never asks for one. Pick the **Runner** the project
is linked to first, then where the repository comes from:

- **Your GitHub repos**: the repositories of the GitHub account the runner's `gh` is signed in to,
  and of the organizations that account belongs to (one `gh repo list` per owner). Each row shows
  the GitHub mark, `owner/name` and its description, and whether it is private, a fork or
  archived and when it was last updated, under a heading per owner (with their GitHub avatar). The
  filter box matches names and descriptions. A repository that is already one of the org's
  projects is marked and can't be picked again.
- **New GitHub repo**: a name (or `owner/name` for an organization) creates an empty repository
  with the runner's `gh`, private unless you tick **public**, and makes the project from it.
- **Remote URL**: any `https://`, `ssh://` or `git@host:path` remote.
- **Local only**: no remote and no GitHub, for throwaway work. The runner makes a new repository
  in a new folder named after the project in its projects folder (in the org's folder there),
  with `git init` and an initial commit, and the project is that runner's own (not shared). Only
  the runner's owner can make one, and a folder of that name that already holds something is
  refused, never taken over. Deleting the project deletes its folder on the runner too (the
  delete dialog says so). To put it on GitHub later, run `gh repo create --source . --push` in
  the folder and share it as a new project.

Listing and creating repositories use the runner owner's GitHub login, so only the runner's owner
can use those two (anyone can paste a URL); the hub has no GitHub login of its own. The name
defaults to the repository's.

### The Projects list

The dashboard's **Projects** list is a project picker: each project is listed once, however many
runners it is linked to, by name. The org's shared projects come first, under **Shared**; a runner's
own (local-only) projects follow under **On <runner>**, one heading per runner, alphabetically.
With only one group (only shared projects, say) there are no headings at all. A project's place in
the list doesn't change when its runners come online, go offline, or are linked or unlinked.

The dot on a project's icon says Claude is working on it (yellow) or work waits for review (green).
A project gets a badge only when its runners need attention:

- **needs a runner**: no runner is linked to it (link one in Project settings → Runners);
- **runner offline** / **all runners offline**: none of its linked runners is online;
- **1/2 online** (and so on): some of its linked runners are offline.

Hovering the badge lists its runners and whether each is online. No badge means every runner it is
linked to is online.

At the bottom of the list, **Runners** opens the Runners page (the same as account menu →
Runners). Its dot sums up the org's runners: green when all are online, amber when some are
offline, red when none is, grey when there are none yet.

## Add a runner

In the hub's dashboard, open the account menu → **Organization** → **Add a runner** (any member
can, in a signed-in browser). It shows a one-time code (valid for 10 minutes) inside a command to
paste on the other computer (Linux or macOS, amd64 or arm64). The code is bound to you and the
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
   Linux, a LaunchAgent (`com.github.klahr.dash-runner`) on macOS. It reconnects by itself,
   waiting longer between attempts while the hub is down (up to a minute). `--no-service` skips
   this and runs the runner in the terminal instead.

The dialog says **Connected ✓** when the runner comes online, then lists the git repositories it
finds in its projects directory to add as [its own projects](#projects-of-a-runners-own).

On Linux a user service stops when you log out unless lingering is on; the installer says so,
and an admin can turn it on with `sudo loginctl enable-linger <you>`.

### Update a runner

Run the command again **without** `--code`:

```sh
curl -fsSL https://dash.example.com/runner/install.sh | sh
```

It installs the hub's version if it differs, keeps the registration (and projects directory),
and restarts the service. A runner set up with `--data` needs the same `--data` again. The Organization page shows each runner's version and flags those that
differ from the hub's, with this command to copy.

### Managing the runner

```sh
dash runner status      # registration, hub, connection, service
dash runner install     # (re)install and start the background service
dash runner uninstall   # stop and remove it; the registration is kept
```

Logs: `journalctl --user -u dash-runner` on Linux, `~/Library/Logs/dash-runner.log` on macOS.

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
`-parallel`, `-claude`, `-claude-args`, `-chat`, `-editor`, …) only matter to a runner; the hub
accepts them, logs a warning that it ignores them, and leaves them out of `dash -h`. It doesn't
listen on a port of its own unless you pass `-addr` (then it also needs `DASH_USERNAME` and
`DASH_PASSWORD`, like a normal Dash).

A runner belongs to whoever made its code, in the org they were working in: its token is one of
their [tokens](users.md#tokens), pinned to that org, and stops working if they leave it. The
Organization page lists the org's runners (other orgs never see them) as online or offline, with
when each was last seen, whose it is, the Dash version it runs, how many tasks it is running and
its owner's Claude usage. It also shows the runner's Claude Code version. If Claude Code changes in a
way Dash relies on, the runner shows a warning, and so does the log of each task it runs. The runner
checks the version and whether `claude -p` still accepts `--output-format stream-json` and
`--permission-prompt-tool` when it starts and every hour. On every run, it checks that the
stream-json events (the session's start, messages, `rate_limit_event`, the result) still have the
fields Dash reads.
Its owner and the org's admins can:

- **Pause** it: it finishes what it is running and gets no new tasks;
- set **At once**, how many of the hub's tasks it runs at a time (0: its own `-parallel`);
- **Revoke** it (there, or under **Tokens & apps**): it is disconnected at once, its token stops
  working (the hub keeps only a hash of it) and it is unlinked from every project. Tasks it was
  running fail and can be queued again on another runner. The runner exits, saying so (its
  systemd service doesn't restart it; launchd retries every 30 s); to bring it back, run the
  install command with a new code (revoking frees its name for that).

Runners registered with an older hub become the env account's.

## Shared projects: tasks on runners

The hub coordinates; the runners do the work. In a shared project's **Settings → Runners**, tick
the runners it is linked to (your own runners, or any of the org's if you're an admin), or tick the
projects on a runner's **Projects…** screen (below). From then on:

- Each task goes to one of the project's runners that is online, not paused, below its limit and
  not held back by its owner's Claude usage, the least busy first. A task that ran before goes
  back to the runner that has its worktree and Claude session.
- If none can take it, the task stays queued and its card says why ("no runner online: laptop is
  offline", "waiting for a runner: laptop is busy (3 of 3 tasks)"). It starts as soon as one can.
- The project needs a git remote: each runner clones it into its projects folder the first time
  it is linked (see [Where a runner keeps a shared project](#where-a-runner-keeps-a-shared-project)),
  and keeps that clone. Work lands on the remote's default branch the same
  way it does without runners, but done by the runner. On GitHub that means a pull request (the
  default): when you approve, the runner pushes the task's branch, opens one and squash-merges it
  once GitHub finds it mergeable and its checks pass (your approval was the review; turn **Merge
  pull requests when checks pass** off to merge on GitHub yourself). If GitHub refuses (a required
  review, say), the pull request stays open for you to merge there. The hub marks the task merged
  once the runner reports it; the hub never needs a checkout, nor `gh`, of its own.
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
  online runners right away, over the connection they already keep open. Each one fetches, and if
  its clone's checkout is on that branch, clean and strictly behind, fast-forwards it. A checkout
  with uncommitted changes or commits of its own is left alone; the project then shows "beta: 2
  commits behind origin" in the sidebar, with a **Pull** button that fast-forwards only and says
  why when it can't. Task worktrees are never touched, and running tasks stay on their runner.
  Runners fetch again whenever they reconnect, and every 5 minutes even when nobody looks at the
  project, so pushes made outside Dash (by hand, or a pull request merged on GitHub) show up too.
  A webhook can be told when a checkout is left behind (`checkout.behind`, off unless you tick
  it; at most once every 6 hours per runner). A GitHub push webhook on the hub could trigger the
  same fan-out for outside pushes; it isn't there yet.
- Projects whose users never chose how work lands follow the default when the hub upgrades or
  their settings change: pull requests on GitHub, merging otherwise. A choice you made stays.
  Tasks with a pull request already open are followed until it is merged or closed, whatever the
  mode.
- Review happens on the hub as usual: the diff, the log and the timeline come from the runner,
  and **Run tests again** runs them on the task's runner, in its worktree there. Attachments and
  editor changes can't be sent to runners yet, and live previews are on the runner's own
  dashboard. A terminal in the task's worktree opens from the hub for the runner's owner when it
  runs with `-hub-commands` (see below).

### A runner's projects

**Projects…** on a runner's row of the Runners page lists what that runner can build, with the
projects it is linked to ticked. It edits the same link as each project's **Settings → Runners**:
a change on one screen shows up on the other right away, in every open browser.

- **For the runner's owner** it lists the repositories of their GitHub login, as **+ New project →
  Your GitHub repos** does (through the runner's `gh`, a heading per owner, with a filter), then
  the org's other shared projects: those whose remote isn't one of those repositories (a remote
  URL, another account's repository).
- **Ticking a repository that is already one of the org's shared projects** links the runner to
  it. If the runner is online it clones (or adopts) it at once, and a remote it can't read says
  so right away, in the words its tasks would wait with ("runner laptop cannot access
  owner/repo over ssh: git@github.com: Permission denied (publickey)"). The link stays either way,
  as from Settings → Runners. An offline runner clones the project when it next gets work for it.
- **Ticking a repository that isn't a project yet** creates the shared project (named after the
  repository) and links the runner to it in the same step. The screen says so: such a project
  is the org's, and everyone in the org sees it and can link their runners to it. For a
  repository only this computer works on, use **Add projects from this computer…** instead (see
  [below](#add-projects-from-a-runners-computer)).
- **Unticking** unlinks the runner only: the project, its tasks and its other runners' links stay,
  and the runner's clone stays on its disk as a project of its own. Unlinking is refused while
  one of the project's tasks is running on that runner (Claude working on it, a review or a
  merge): "1 task of web is running on laptop: wait for them to finish (or cancel them) before
  unlinking it". This also applies in Settings → Runners. Tasks that have finished running
  there (in review, failed, or waiting to continue) don't block it; the screen counts them and
  asks first. Their worktrees stay on that computer, and when queued again they start afresh on
  another runner linked to the project.
- **Build all the org's projects** (for a new computer) links the runner to every shared project
  it isn't linked to yet. An online runner then clones them one after another in the background.

Linking follows the same rules as in Settings → Runners: you can link your own runners, and admins
can link any runner in the org. On this screen the same people can unlink, because it changes
what that runner builds. Anyone in the org can open the screen to see
what a runner builds. Only the runner's owner can list GitHub repositories or create a project
from one, because the list comes from their GitHub login and the hub has none of its own. When an
admin opens someone else's runner, the screen shows only the org's existing shared projects and
says why.

### Everything else about a shared project also runs on a runner

The hub has no clone of a shared project, so whatever needs one is forwarded, over the runner's
connection, to the clone of one of the project's runners that is online:

- the git views: **History** (the log and graph), branches and switching the working branch
  (made and pushed by the runner), the default branch's state against origin, **Push**, pull
  requests from the working branch, release tags (**Tag release…** and the `create_release` tool);
- the file browser and the `CLAUDE.md` and settings files;
- the project's commands: **Run**, **Test** and **Deploy**, also in a task's worktree (they run on
  that task's runner). The runner runs the commands set on its clone; started with `-hub-commands`, it takes
  the shared project's commands from the hub (set in its settings on the hub), each time the
  project is linked;
- the **Shell** view and **Open shell in this worktree** on a task: a terminal on the runner, in
  the project's checkout there or the task's worktree, only for the user who registered the runner
  and only when it runs with `-hub-commands` (a shell can do whatever a deploy command can). The
  hub chooses neither the runner's folder nor whose terminal it is: the runner checks its owner
  itself. Without the flag, or for the org's other members, the Shell view says why;
- `!` commands in a project's or a task's chat: likewise only with `-hub-commands`, for the user
  who registered the runner, and only when its chat runs commands (`-chat-tools shell`; never
  with `-chat api`);
- GitHub Actions: the hub's **CI** view, the automatic investigation of a failing build, and
  **Retry** read and re-run the builds with `gh` on a runner;
- **Clean up** cleans the runner's clone.

When none of the project's runners is online these fail at once with "No runner is online for this
project. Start one with `dash runner`." (the dashboard also says so above the board); nothing falls
back to the hub. A project no runner is linked to says it **needs a runner**, with a **Link a
runner** button.

The hub never runs tasks itself: a task of a project not linked to a runner waits, saying a runner
must be linked. (`-hub-tasks` is gone; an old start command that still passes it starts, logs a
warning and ignores it.)

### Moving projects off the hub

A hub from before this kept projects of its own, in its own `-projects` directory. They are kept,
with their tasks, issues and history on the board, and the hub deletes nothing on disk; at
startup it logs a warning for each project no runner is linked to (`project needs a runner`), and
the dashboard marks them **needs a runner**. Their work waits until you do one of these, per
project:

1. **Link it to a runner** (Project settings → Runners), if it has a git remote: the runner clones
   the remote and the project carries on as a shared project. A project without a remote can't be
   linked: push it somewhere first (e.g. create the GitHub repository from the folder on disk with
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
owner's subscription). So **a hub needs at least one runner**: start `dash runner` on a computer
with git and Claude Code logged in (account menu → **Runners** → **Add a runner** gives the
command). Until one is online, the dashboard says so where Claude would be used. The hub needs
**no `claude` install, no `-claude` flag, no login and no API key**; it never starts `claude` and
never reads `ANTHROPIC_API_KEY`. Runners
keep no board of their own for the hub's work: what Claude changes on the board through Dash's
tools (issues it reports, tasks it creates) goes straight to the hub's.

Where each kind of Claude work runs:

- **Tasks**: on one of the project's runners (see [above](#shared-projects-tasks-on-runners)).
  A task of a project with no runner linked waits, saying a runner must be linked.
- **Scans**: on one of the project's runners online, in a throwaway checkout of its clone at
  origin's default branch. The hub keeps the runs, their logs and the issues they file; the run's
  log says which runner it ran on. With the project's runners all offline a run waits, showing
  "no runner online: laptop is offline", and starts when one comes back; a project with no runner
  linked can't be scanned. Repository scans (`.dash/scans/`) are used only by a runner's own
  projects; hub projects use built-in and Dash scans.
- **Project and task chats**: on one of your runners linked to the project (a task's chat on the
  runner that ran it). Shell commands typed in them with a leading `!` run there too, in the same
  folder, never on the hub: see [Chat](chat.md#shell-commands-with-).

- **General chat**: runs on one of your runners, in a scratch folder (`<-data>/chat` on that
  computer), not in any project. The chat's header says which runner it runs on; with several
  online you pick one there, and with none online it says so. The hub keeps the conversation, as
  before. Files can't be attached to it yet.
- **Titles and summaries**: a task created without a title is named by Claude on one of your
  runners; with none online it is named after the first line of its instructions. A task's
  summary is written by the runner that ran it.
- **Clean up** and **Split…** (in the task form): run on one of your runners; with none online
  they say so.

Nothing falls back to running Claude on the hub: work with no runner to go to waits (tasks,
scans) or says so (chats, clean-up, split).

A runner does these only for its owner, the user whose code registered it: the hub sends a
user's calls only to that user's own runners in the org they're working in (never another
member's, admins included), and the runner checks the user the hub names against the one it was
registered by. Runners registered with a hub older than this can't check, and trust the hub; run
the install command with a new code to register them again.

Their Claude usage is the runner's: it shows with the runner's usage on the Organization page,
never as the hub's.

This used to be a choice (`-hub-claude`, with a single-user hub running Claude itself by default).
The flag is gone: an old start command that still passes it starts, logs a warning and ignores it.
Claude's flags (`-claude`, `-claude-args`, `-chat`, `-title-model`, …) are a runner's: the hub
ignores them, with a warning.

### Laptops that sleep

If a runner disconnects while running a task (the lid closes, the Wi-Fi drops), the hub waits
`-runner-grace` (default 2 minutes) for it to come back. After that the task fails, "interrupted:
laptop went offline mid-task", and its **Continue on laptop** button queues it again: the runner
picks up its worktree and Claude session where it stopped. If the runner kept going meanwhile (a
short outage), it says so when it reconnects and the task carries on. A runner that can't reach
the hub for its `-offline-grace` stops its tasks itself (below).

### What a runner accepts from the hub

A runner takes work only for projects its hub linked to it, of the organization it was registered
for. It keeps the hub's id for the project with its checkout and finds it by that id, never by a
path from the hub: the hub sends a remote, and the runner picks the folder itself, inside its
`-projects` (it clones only `https://`, `ssh://` or `user@host:path` remotes, never a local path).
It refuses and logs anything else; the hub reaches only tasks it sent, by its own ids.

### How a runner reaches GitHub

A runner clones a shared GitHub project over **SSH** by default (`git@github.com:owner/repo.git`,
with the computer's SSH keys), whatever form the hub's remote has. Start it with
`-github-protocol=https` (or `DASH_GITHUB_PROTOCOL=https`) to use `https://github.com/owner/repo.git`
instead; private repositories then need a git credential helper on that computer, e.g.
`gh auth setup-git`, or a personal access token. The clone's origin is that URL, so fetches and
pushes use the same protocol. Clones made before keep their origin.

A project can instead have a **clone URL** (New project → Clone URL, or `clone_url` in the API),
which every runner clones as it is: a remote that isn't GitHub's, or an SSH host alias for a deploy
key (`git@github-deploy:owner/repo.git`). It is a URL only; credentials in it are refused, and stay
on the runner.

Before cloning, the runner checks it can read the remote (`git ls-remote`, never prompting:
`GIT_TERMINAL_PROMPT=0`, ssh in batch mode) and otherwise fails the link with, for example,
"runner laptop cannot access owner/repo over ssh: git@github.com: Permission denied (publickey)".
An empty repository (just created on GitHub) passes; the runner gives it a first commit on the
project's default branch, whatever the computer's `init.defaultBranch`, and pushes it.

Creating a GitHub repository (New project → New GitHub repo) still uses the runner's `gh` login,
whichever protocol it clones over.

### Where a runner keeps a shared project

When a project is linked to it, a runner puts it straight in its projects folder, named after the
remote: `https://github.com/klahr/dash.git` becomes `~/src/klahr/dash` for a runner started
with `-projects ~/src/klahr`.

- **A clone of the same repository is already there** (the remote compared without caring about
  `https` or `ssh`, credentials or a trailing `.git`): the runner adopts it instead of cloning.
  If it was one of the runner's own projects, that project becomes the shared one (unlinking
  makes it the runner's own again).
- **Something else is there** (another repository, a folder that isn't one, a file, a symlink, or
  a clone of the same remote already linked to another shared project): it is never touched; the
  runner takes the next free name, `dash-2`, `dash-3`, …
- **Your own edits in an adopted clone**: tasks still work in worktrees, but landing one moves the
  checked-out branch of that folder. So while it has uncommitted changes to tracked files, a task
  you approved waits ("Waiting for you to commit or stash …") and lands once you commit or stash
  them; nothing is pushed meanwhile and your changes are never overwritten. Starting the project's
  run or test command there warns about them; its deploy command is refused until they are
  committed. Switching the working branch only makes branches; it doesn't touch the folder.
- **Runners from before** cloned shared projects into `<-projects>/hub/<name>`. Those stay where
  they are, with their worktrees, task branches and uncommitted work: on its first start after the
  upgrade the runner marks them as the hub's once and from then on finds them by id. Only new
  links go to `<-projects>/<repo>`. To move one, unlink it, move the folder, and link it again
  (the runner adopts it at its new place).

## Projects of a runner's own

A runner's own projects (those in its `-projects` that aren't linked from the hub) show in the
hub's dashboard for platform admins, under **On <runner>**, and their tasks, git and commands run
on that computer. Notifications come from all of them. The hub has no heading of its own in the
**Projects** list: its projects are the shared ones, under **Shared** (see
[The Projects list](#the-projects-list)). A runner's own projects can only be listed while it is
online.

### Add projects from a runner's computer

Right after a runner connects, the **Add a runner** dialog lists the git repositories in its
projects directory; later, **Add projects from this computer…** on the runner's row of the
Organization page lists them again. Each shows its folder name, its path in the projects directory,
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

## What a runner lets the hub do

A runner only does the dashboard's normal work on its own projects: tasks, reviews, merges,
issues, scans, chats about a project or task, CI, previews and permission prompts. It refuses:

- terminals and ad-hoc commands, on its own projects and on its clones of shared ones (with
  `-hub-commands` it takes ad-hoc commands, and opens terminals for its owner only);
- setting a project's run, test, deploy or setup command, or taking a shared project's from the
  hub (set those on the runner, or start it with `-hub-commands` if you trust the hub to run
  commands on it);
- adding a project outside its `-projects` directory;
- the general chat and Claude's short calls (titles, summaries, clean-up, split) for anyone but its owner
  (see [above](#claude-runs-on-each-users-own-runner));
- chats, and the shell commands typed in them with `!` ([Chat](chat.md#shell-commands-with-)), for
  anyone but its owner, and `!` commands at all when its chat is read-only (`-chat-tools readonly`);
- listing the repositories in its projects directory, or adding them, for anyone but its owner
  (see [above](#add-projects-from-a-runners-computer)).

Claude still runs tasks with the runner's `-claude-args` and asks for permission as usual; the
runner reports the prompts of the hub's tasks to the hub, so they appear in the hub's dashboard
(and its notifications), and the hub sends your answer back to the runner. While the runner is
offline, its prompts aren't listed on the hub; they show again when it reconnects.

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
