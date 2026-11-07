# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. The Dash you open in the browser is the **hub**. The other
computers are **runners**: they connect out to the hub and keep that connection open, so they
need no open port, VPN or shared secret. The hub must be reachable over HTTPS from the runners,
see [Deploy the hub](deploy.md).

## Add a runner

1. In the hub's dashboard, open the account menu → **Runners** → **Add a runner**. It shows a
   one-time code (valid for 10 minutes) and the command to run.
2. On the other computer, from its Dash checkout (it needs `claude` logged in, like any Dash):

   ```sh
   ./bin/dash runner --server https://dash.example.com --code ABCD-EFGH
   ```

   The runner is named after its host; add `-name work` to pick another name. It swaps the code
   for its own token, saved in `~/.local/share/dash/runner.json` (readable only by you), and
   connects. From then on just run `./bin/dash runner`; it reconnects by itself, waiting longer
   between attempts while the hub is down (up to a minute).

A runner takes the same flags as `dash` (`-projects`, `-parallel`, `-claude-args`, …). It doesn't
listen on a port of its own unless you pass `-addr` (then it also needs `DASH_USERNAME` and
`DASH_PASSWORD`, like a normal Dash).

Projects are grouped by computer, and tasks run on the computer that has the project.
Notifications come from all of them.

The Runners page lists each runner as online or offline, with when it was last seen. **Revoke**
disconnects a runner at once and its token stops working (the hub keeps only a hash of it). The
runner then exits, saying so; to bring it back, register it again with a new code.

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

[Live previews](previews.md) run on the computer that has the project and are served by that
computer's Dash, so a runner can serve them only if it has a listener (`-addr`) reachable over
HTTPS and its own `-preview-domain`.

## -peer (deprecated)

Before runners, the hub connected to the other computers itself: each ran a normal Dash reachable
over HTTPS from the hub (see [Deploy the hub](deploy.md): each behind its own Caddy or Cloudflare
Tunnel hostname, or on the same tailnet), all with the same `DASH_PEER_TOKEN`, and the hub was
started with `-peer work=https://work.example.com`. The hub calls its peers, not the other way
round, so a hub on a VPS can show computers at home through their tunnels; give peers hostnames
outside the hub's preview domain (not `work.preview.dash.example.com`). This still works in this
release, with a warning, and will be removed in the next one. To move a computer over, stop it, add it as a
runner, and remove its `-peer` flag from the hub; its projects and tasks stay as they are.
