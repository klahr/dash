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
  Runners page, or start a new one with **+ Add project** → **Local only**. See [below](#projects-of-a-runners-own).
- **A shared project**: the hub's project, a git remote that one or more runners are linked to.
  Each linked runner keeps its own clone of the remote, in its organization's folder inside its
  projects folder (`<-projects>/<org>/<owner>/<repo>`), any of them can run its tasks, and work lands on the remote. It shows in
  the dashboard under each runner it is linked to, with a small "shared" mark (two people). Make
  one with **+ Add project** (see [Add project on the hub](#add-project-on-the-hub)): it is linked
  to the runner picked beside the dialog's title. Link more runners in its **Settings → Runners**, or pick
  the projects a runner builds from the runner's side, with **Projects…** on its row of the
  Runners page (see [A runner's projects](#a-runners-projects)).
  See [below](#shared-projects-tasks-on-runners).

A project that is only ever worked on from one computer is simplest as that runner's own; share
it when several people's runners (or several of yours) should take its tasks.

### Add project on the hub

The hub keeps no folders, so **+ Add project** never asks for one. The dialog lists where the
repository comes from down its side (across the top on a phone), and beside it only the fields for
that choice. With more than one runner you may link projects to, pick it beside the title (yours
are marked "(yours)"); with one, there is nothing to pick.

- **From GitHub** (the default): the repositories of the GitHub account the runner's `gh` is signed in to,
  and of the organizations that account belongs to (one `gh repo list` per owner). Each row shows
  the GitHub mark, `owner/name` and its description, and whether it is private, a fork or
  archived and when it was last updated, under a heading per owner (with their GitHub avatar). The
  filter box matches names and descriptions. A repository that is already one of the org's
  projects is marked and can't be picked again.
- **New GitHub repository**: a name (or `owner/name` for an organization) creates an empty
  repository with the runner's `gh`, private unless you tick **Public**, and makes the project from it.
- **From URL**: any `https://`, `ssh://` or `git@host:path` remote.
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
and the start server, test and deploy commands; the commands can be set later in Project settings
too. The button says what it will do (**Add owner/repo**, **Create repository and add**, **Add
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

On the Runners page, an out-of-date runner has an **Update to …** button for its owner and the
organization's admins. The runner goes into maintenance mode: the hub sends it no new tasks or
scans (they wait, saying "*name* is updating", or go to the project's other runners), and the
tasks it is running finish. Its page shows "Waiting for N tasks to finish…" until then; the update
can be cancelled until it starts. Then the hub tells the runner to update: it downloads the hub's
release from GitHub, checks it against the release's `checksums.txt`, keeps its binary as
`dash.prev` and puts the new one in its place, and restarts on it with the same flags (`--data`
included):

- under its background service it exits with status 4, and systemd (`Restart=on-failure`) or
  launchd (`KeepAlive`) starts the new binary;
- started by hand (installed with `--no-service`), it replaces its own process with the new binary
  (`exec`), in the same terminal.

Maintenance mode is the hub's, so it survives the runner's reconnects and the hub's restarts. It
ends when the runner connects running the hub's version, or when the update fails: the runner can't
write its binary (installed by root into `/usr/local/bin`, say: update it the way it was
installed), the download or its checksum is wrong (the old binary stays), or the runner comes back
on its old version. The Runners page then shows why. The runner's own Pause is separate and stays
as it was.

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
and restarts the service. A runner set up with `--data` needs the same `--data` again. The Organization page shows each runner's version and flags those that
differ from the hub's, with this command to copy.

### Managing the runner

```sh
dash runner status      # registration, hub, connection, service
dash runner install     # (re)install and start the background service
dash runner uninstall   # stop and remove it; the registration is kept
dash runner update      # install the hub's version (or --version X) and restart the service
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
- **Revoke** it (there, or under **Account → Tokens & apps**): it is disconnected at once, its token stops
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

### A runner's projects

**Projects…** on a runner's row of the Runners page lists what that runner can build, with the
projects it is linked to ticked. It edits the same link as each project's **Settings → Runners**:
a change on one screen shows up on the other right away, in every open browser.

- **For the runner's owner** it lists the repositories of their GitHub login, as **+ Add project →
  From GitHub** does (through the runner's `gh`, a heading per owner, with a filter), then
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
  that task's runner). The runner always takes the shared project's commands from the hub (set
  in its settings on the hub), each time the project is linked;
- the **Shell** view and **Open shell in this worktree** on a task: a terminal on the runner, in
  the project's checkout there or the task's worktree, only for the user who registered the runner
  (a shell can do whatever a deploy command can). The hub chooses neither the runner's folder nor
  whose terminal it is: the runner checks its owner itself. For the org's other members, or on a
  runner older than the hub, the Shell view says why;
- `!` commands in a project's or a task's chat: likewise only for the user who registered the
  runner, and only when its chat runs commands (`-chat-tools shell`; never with `-chat api`);
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

### Shared notes between a project's runners

The runners linked to a shared project keep a shared notes space for it (package `internal/notes`).
Each runner has a full copy in `<-data>/shared/<hub project id>.db`, so it reads its notes locally,
even while the hub can't be reached. A runner saves a note locally first, then sends it to the hub,
sealed with the project's key. The hub passes it on to the project's other runners that are online
and keeps nothing: no copy, no log of what it relayed, and no page or API that shows a note. Its
proxy to runners (`/peer/…`) can't reach their notes either.

Each write gets a stamp (its runner and that runner's counter). Copies merge last-writer-wins per
note, so every runner ends up with the same notes and concurrent writers never wait for each other.
When a runner reconnects, it tells the others how far it has got with each writer. They send what it
lacks, and it sends them what it wrote while it was away. A note is at most 8 KiB; a bigger one is
refused. A note that looks like it holds a credential is refused on the runner that writes it,
before it is saved or sent.

A Dash that runs tasks itself, without runners, has no shared notes.

#### Tidying

Each runner tidies its own copy when it starts and then about hourly (`notes.Service.RunTidying`).
The hub and tasks play no part. `Service.Tidy` runs a tidy right away; this is what "remove expired"
calls. Every rule uses only synced data (update times, expiries, titles, ids), never when a runner
read a note. Anything a tidy changes is an ordinary write that syncs to the other runners, so the
copies converge even when runners tidy at different moments.

- **Expiry.** A note expires 30 days after its last write unless its writer sets another expiry or
  marks it permanent. Confirming a note (`Confirm`) moves its expiry forward, on every copy.
  Expired notes are purged.
- **Caps.** A project holds at most 500 live notes and 1 MiB of them. A write that goes past a cap
  doesn't fail. Instead, the notes updated longest ago are evicted, never permanent ones. Evicted
  notes become tombstones. A write is refused only if permanent notes alone fill the project.
- **One note per title.** Writing a title that already exists (ignoring case and surrounding
  spaces) updates that note. If two runners create the same title before hearing of each other, the
  note created first stays (ties go to the smaller id). It takes the newest content of the two and
  stays permanent if either was. The other note becomes a tombstone.
- **Clears.** A project-wide "delete everything updated before T" (`Clear`) is one synced tombstone.
- **Retention.** Tombstones and clears are kept for 60 days, then purged. The database is compacted
  (`VACUUM`) about once a day.

A runner that hasn't caught up with the hub for longer than the retention window has a stale copy.
Its copy could bring back notes the others deleted and have since purged. So it doesn't answer other
runners' catch-ups and doesn't tidy by writing. On reconnect it discards its copy, including what it
wrote while away, and takes a full copy from a peer. If every runner it reaches is stale too, the
first one to catch up keeps its own copy and the others resync from it.

The TTL, caps and retention window can be set per project (`Service.Policies`). Use the same values
on all of a project's runners.

#### Encryption and approving runners

Shared notes are encrypted end to end. Only runners hold keys. The hub, its web pages and browsers
never get a project key or see a note in plain text: all the hub sees is ciphertext, the project,
the message size, the runner that sent it, and the runners' public keys.

- Each runner makes a key pair the first time it starts (`<-data>/notes-identity.json`, readable by
  its user only, next to `runner.json`). It tells the hub its public key, and the hub shows its
  fingerprint on the runner's page.
- Each project has a random key that seals every note between its runners (NaCl secretbox). A
  runner gives it to another runner by wrapping it for that runner's public key (NaCl box). The
  wrapped copy travels through the hub like any other message.
- A runner wraps its project keys only for runners whose fingerprint you approved **on it**, and
  takes keys only from runners approved on it. Approve each pair of runners both ways. Compare the
  fingerprint with what the other runner shows itself, not only with the hub's page: the hub relays
  the keys and could swap in its own.

  ```sh
  dash runner notes                          # this runner's fingerprint, approved runners, runners waiting
  dash runner notes approve ABCD-EFGH-JKLM-NPQR-STUV
  dash runner notes revoke  ABCD-EFGH-JKLM-NPQR-STUV
  ```

  A runner whose key isn't approved yet shows under "Waiting for your approval", and its log says
  so with the command to run. Notes written before two runners trade keys reach each other once
  they do.
- A runner pins the key it first sees for each runner id. If that runner later shows up with
  another key, it is refused and the runner logs an error, and `dash runner notes` lists the change.
  If the other runner really got a new key (its data folder was lost), approve the new fingerprint
  after checking it on that runner.
- When a runner leaves a project (unlinked, or revoked on the hub), the runners that remain notice
  the next time they talk through the hub (when a note is written, and at least every 15 minutes).
  They rotate to a new project key, wrap it for each other, and seal new notes with it. A message
  sealed with an older key is refused. `dash runner notes revoke` rotates at once, even if the hub
  still lists the runner. Revoke it on every runner that approved it. Runners keep their notes in
  plain text in their own copy, so nothing has to be re-encrypted. A runner with the new key gets
  every note again, sealed with that key, when it next catches up.
- The secret scanner runs on the writing runner before a note is saved or sealed.

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

By default (`-github-protocol=auto`) a runner clones a shared GitHub project over **SSH**
(`git@github.com:owner/repo.git`, with the computer's SSH keys), whatever form the hub's remote has.
When SSH can't read the repository (no key GitHub accepts, an unknown host key) and the GitHub CLI
is installed and logged in (`gh auth login`), it clones over **HTTPS**
(`https://github.com/owner/repo.git`) instead; when neither works the link fails with both reasons,
e.g. "SSH: Host key verification failed; HTTPS: gh isn't logged in (run `gh auth login`)".
`-github-protocol=ssh` or `-github-protocol=https` (or `DASH_GITHUB_PROTOCOL`) forces one, with no
fallback. The clone's origin is the URL it was cloned from, so fetches and pushes use the same
protocol. Clones made before keep their origin.

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

When a project is linked to it, a runner puts it in the folder of the project's organization
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
  the shared one (unlinking makes it the runner's own again). A clone anywhere else, such as your
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
  run or test command there warns about them; its deploy command is refused until they are
  committed. Switching the working branch only makes branches; it doesn't touch the folder.
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
  move one, unlink it, move the folder into `<-projects>/<org>/`, and link it again (the runner
  adopts it at its new place).

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

- terminals for anyone but its owner (it always takes the hub's project commands and ad-hoc
  commands, and opens terminals for its owner);
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
