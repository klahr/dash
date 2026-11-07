# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. The Dash you open in the browser is the **hub**. The other
computers are **runners**: they connect out to the hub and keep that connection open, so they
need no open port, VPN or shared secret. The hub must be reachable over HTTPS from the runners,
see [Deploy the hub](deploy.md).

## Add a runner

In the hub's dashboard, open the account menu → **Runners** → **Add a runner**. It shows a
one-time code (valid for 10 minutes) inside a command to paste on the other computer (Linux or
macOS, amd64 or arm64):

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
and restarts the service. The Runners page shows each runner's version and flags those that
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

Projects are grouped by computer, and tasks run on the computer that has the project.
Notifications come from all of them.

A runner belongs to whoever made its code, in the org they were working in: its token is one of
their [tokens](users.md#tokens), pinned to that org, and stops working if they leave it. The
Runners page lists each runner as online or offline, with when it was last seen, whose it is and
the Dash version it runs. **Revoke** (there, or under **Tokens & apps**) disconnects a runner at
once and its token stops working (the hub keeps only a hash of it). The runner then exits, saying
so (its systemd service doesn't restart it; launchd retries every 30 s); to bring it back, run
the install command with a new code (revoking frees its name for that). Making codes and revoking
runners is for platform admins, in a signed-in browser (not with an API token). Runners
registered with an older hub become the env account's.

## What a runner lets the hub do

A runner only does the dashboard's normal work on its own projects: tasks, reviews, merges,
issues, scans, chats about a project or task, CI, previews and permission prompts. It refuses:

- terminals and ad-hoc commands;
- setting a project's run, test, deploy or setup command (set those on the runner, or start it
  with `-hub-commands` if you trust the hub to run commands on it);
- adding a project outside its `-projects` directory;
- the general chat, which would run in its home directory (it runs on the hub instead).

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
