# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. The Dash you open in the browser is the **hub**. The other
computers are **runners**: they connect out to the hub and keep that connection open, so they
need no open port, VPN or shared secret. The hub must be reachable over HTTPS from the runners,
see [Deploy the hub](deploy.md).

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
   projects live. It swaps the code for its own token, saved in `~/.local/share/dash/runner.json`
   (readable only by you);
4. installs and starts a background service: a systemd **user** unit (`dash-runner.service`) on
   Linux, a LaunchAgent (`com.github.klahr.dash-runner`) on macOS. It reconnects by itself,
   waiting longer between attempts while the hub is down (up to a minute). `--no-service` skips
   this and runs the runner in the terminal instead.

The dialog says **Connected ✓** when the runner comes online.

On Linux a user service stops when you log out unless lingering is on; the installer says so,
and an admin can turn it on with `sudo loginctl enable-linger <you>`.

### Update a runner

Run the command again **without** `--code`:

```sh
curl -fsSL https://dash.example.com/runner/install.sh | sh
```

It installs the hub's version if it differs, keeps the registration (and projects directory),
and restarts the service. The Organization page shows each runner's version and flags those that
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

A runner takes the same flags as `dash` (`-projects`, `-parallel`, `-claude-args`, …). It doesn't
listen on a port of its own unless you pass `-addr` (then it also needs `DASH_USERNAME` and
`DASH_PASSWORD`, like a normal Dash).

A runner belongs to whoever made its code, in the org they were working in: its token is one of
their [tokens](users.md#tokens), pinned to that org, and stops working if they leave it. The
Organization page lists the org's runners (other orgs never see them) as online or offline, with
when each was last seen, whose it is, the Dash version it runs, how many tasks it is running and
its owner's Claude usage.
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

The hub coordinates; the runners do the work. In a project's **Settings**, tick the runners its
tasks should run on (your own runners, or any of the org's if you're an admin). From then on:

- Each task goes to one of the project's runners that is online, not paused, below its limit and
  not held back by its owner's Claude usage, the least busy first. A task that ran before goes
  back to the runner that has its worktree and Claude session.
- If none can take it, the task stays queued and its card says why ("waiting for a runner: laptop
  is offline"). It starts as soon as one can.
- The project needs a git remote: each runner clones it into `<-projects>/hub/` the first time it
  gets one of its tasks, and keeps that clone. Work lands through the remote: the runner pushes
  the task's branch and opens a pull request when you approve, so **merging locally on the hub is
  off** for linked projects.
- Review happens on the hub as usual: the diff, the log and the timeline come from the runner.
  Attachments and editor changes can't be sent to runners yet, and live previews, the worktree
  shell and re-running tests are on the runner's own dashboard.

The hub doesn't run tasks itself once runners are configured. `-hub-tasks` sets this: `auto` (the
default) runs the tasks of unlinked projects on the hub only while no runner is registered, `on`
always does (a single-user install with a runner or two for some projects; add `-hub-claude on`
too, since the hub otherwise stops running Claude once a runner registers), and `off` never does
(those tasks wait, saying a runner must be linked). With `-hub-claude off` (below) the hub runs no
tasks whatever `-hub-tasks` says.

## Claude runs on each user's own runner

A hub with several users, or with runners, doesn't run Claude at all: every use of Claude is
billed to the user who asked for it, through their own runner and their own `claude` login (their
own subscription). The hub needs **no `claude` install and no API key**; it never starts `claude`
and never reads `ANTHROPIC_API_KEY`.

- **General chat**: runs on one of your runners, in a scratch folder (`<-data>/chat` on that
  computer), not in any project. The chat's header says which runner it runs on; with several
  online you pick one there, and with none online it says so. The hub keeps the conversation, as
  before. Files can't be attached to it yet.
- **Titles and summaries**: a task created without a title is named by Claude on one of your
  runners; with none online it is named after the first line of its instructions. A task's
  summary is written by the runner that ran it.
- **Clean up** (in the task form): runs on one of your runners; with none online it says so.

A runner does these only for its owner, the user whose code registered it: the hub sends a
user's calls only to that user's own runners in the org they're working in (never another
member's, admins included), and the runner checks the user the hub names against the one it was
registered by. Runners registered with a hub older than this can't check, and trust the hub; run
the install command with a new code to register them again.

Their Claude usage is the runner's: it shows with the runner's usage on the Organization page,
never as the hub's.

`-hub-claude` sets this: `auto` (the default) lets the hub run Claude itself only while it has a
single user and no runner, so a single-user Dash on one computer works as it always did; `off`
never does; `on` always does, billing everything to the hub's own `claude` login (or, with `-chat
api`, its `ANTHROPIC_API_KEY`). Project and task chats of projects on the hub, and scans of them,
need Claude on the hub: with it off they say so.

### Laptops that sleep

If a runner disconnects while running a task (the lid closes, the Wi-Fi drops), the hub waits
`-runner-grace` (default 2 minutes) for it to come back. After that the task fails, "interrupted:
laptop went offline mid-task", and its **Continue on laptop** button queues it again: the runner
picks up its worktree and Claude session where it stopped. If the runner kept going meanwhile (a
short outage), it says so when it reconnects and the task carries on. A runner that can't reach
the hub for its `-offline-grace` stops its tasks itself (below).

### What a runner accepts from the hub

A runner takes work only for projects its hub linked to it, of the organization it was registered
for, and only for its own clone inside `<-projects>/hub/` (it picks that folder itself, and clones
only `https://`, `ssh://` or `user@host:path` remotes, never a local path). It refuses and logs
anything else; the hub reaches only tasks it sent, by its own ids.

## Projects of a runner's own

A runner's own projects (those in its `-projects` that aren't linked from the hub) still show in
the hub's dashboard for platform admins, grouped by computer, and their tasks run on that
computer. Notifications come from all of them.

## What a runner lets the hub do

A runner only does the dashboard's normal work on its own projects: tasks, reviews, merges,
issues, scans, chats about a project or task, CI, previews and permission prompts. It refuses:

- terminals and ad-hoc commands;
- setting a project's run, test, deploy or setup command (set those on the runner, or start it
  with `-hub-commands` if you trust the hub to run commands on it);
- adding a project outside its `-projects` directory;
- the general chat and Claude's short calls (titles, summaries, clean-up) for anyone but its owner
  (see [above](#claude-runs-on-each-users-own-runner)).

Claude still runs tasks with the runner's `-claude-args` and asks for permission as usual; the
prompts appear in the hub's dashboard.

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
previews it started itself; the hub can't ask it for any other port. Previews on a `-peer` are
still served by that computer's own Dash.

## -peer (deprecated)

Before runners, the hub connected to the other computers itself: each ran a normal Dash reachable
over HTTPS from the hub (see [Deploy the hub](deploy.md): each behind its own Caddy or Cloudflare
Tunnel hostname, or on the same tailnet), all with the same `DASH_PEER_TOKEN`, and the hub was
started with `-peer work=https://work.example.com`. The hub calls its peers, not the other way
round, so a hub on a VPS can show computers at home through their tunnels; give peers hostnames
outside the hub's preview domain (not `work.preview.dash.example.com`). This still works in this
release, with a warning, and will be removed in the next one. To move a computer over, stop it, add it as a
runner, and remove its `-peer` flag from the hub; its projects and tasks stay as they are.
