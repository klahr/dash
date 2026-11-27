# Live previews

When a project has a start server command (`run_command`, which gets `$PORT`), tasks in review
get a **Preview** button: it starts that command in the task's worktree and shows the app next to
the task (full screen on a phone). The board marks running previews **Live**, and **Previews (n)**
in the [status popover](status.md) lists them all, with Stop all. One preview runs per task. It stops when the task is
approved, sent back or merged, and after 30 minutes without traffic (`-preview-idle`). If the
server crashes, its log opens, and **Rework with this log…** hands it to Claude as rework notes.

To mark up a preview and send the picture back to Claude, see [Draw](draw.md). To get pictures of
the changed pages in Review without starting the preview, see [Before/after screenshots](screenshots.md).

## Terminal previews (CLI tools)

A command-line tool never opens a port, so set **Preview tasks in** to *a terminal* in
**Project settings → Commands** (`preview_kind: "terminal"`). The preview is then a terminal in the task's
worktree: the **terminal preview command** (`preview_command`) runs there, or a plain shell when
it is empty. `./.dash-bin` (git ignores it) is on `PATH` and in `$DASH_BIN`, so for example

    go build -o $DASH_BIN/tool . && exec $SHELL

leaves you at a prompt where `tool` is the task's build. Such a project needs no `run_command`.
The preview is live at once, has no URL, and is attached to over the WebSocket in its
`terminal_url` (`/ws/todos/<task>/preview`, the same protocol as other terminals). For a hub's
task the hub passes it through to the task's runner, which opens it only for the runner's owner. It stops like a web preview, with keystrokes counting as traffic. If the command
fails or the shell exits, it shows as crashed; `GET /api/todos/<task>/preview/log?lines=N` returns
its last lines as plain text, for **Rework**.

In the web client, **Preview** on such a task opens that terminal beside the task (full screen on
a phone, with a row of Esc, Tab, Ctrl-C, Ctrl-D and arrow keys). Draw doesn't apply; instead
**Rework with transcript…** puts the terminal's last 200 lines into the rework notes, or only
what you selected in the terminal if you selected something.

### Code → Shell at a commit (Try): the project itself, at any commit

**Code → Shell** has a picker: **main** is a shell in the project's checkout, and **commit…**
(of a CLI project on a hub; `:try`) opens the same kind of terminal as above on the project
rather than a task, to test it without one in review. Its address is `/p/<project>/try`, so older
links to Try still open it. It always
runs on a runner, never on the hub: one that is connected, has the labels the project runs on and opens
terminals for you (only for the runner's owner). With one such runner it is
picked by itself; with several, pick one in the Shell's header; with none, **commit…** is greyed out and
says why.

On that runner Dash fetches the default branch from origin and makes a **detached worktree** of its
clone (under `<data>/try/`), never touching the clone's own checkout, and runs `preview_command`
there with `$DASH_BIN` as above. The header shows the runner and the commit (short hash and
subject); tap the commit to pick another from the branch's history (searchable, the latest 100
matches, or type any hash, tag or branch). Picking one, or another runner, replaces the session and
its worktree. A project has one try session; **commit…** shows it again while it runs. It stops after 30
minutes without typing (or with **Stop**), and its worktree is removed.

### Run and Terminate: the latest commit in one click

A CLI project has a **Run** button (▶) in the header, beside the project's name. It runs
`preview_command` at the tip of the default branch, as **Code → Shell** at a commit does, and opens its
terminal.

- **Run** runs on the runner you used last, or on the only runner that can run it. With no such
  runner yet, it opens the list.
- **▾** lists the project's runners, connected or not. Those that can't run it are greyed out;
  hover one to see why. **Pick a commit…** below them opens the commit picker, and **Configure…**,
  last, opens **Project settings → Commands**, where **Preview tasks in** and the preview command are.
- Dash remembers the runner you picked, per project, in this browser.
- With no runner at all, **Run** is greyed out and says why.

A web project with a start server command has the same button, with a
[commit preview](#preview-a-commit-from-history) instead of a terminal:

- **Run** previews the tip of the default branch on a runner. It opens **History** on that commit
  with the app beside it, as **Preview** there does (full screen on a phone, with a button that
  leads back). `Esc` closes the pane; the preview keeps running.
- When a newer commit lands on the default branch, the preview's bar shows **New commit abc1234 ·
  Update** (on a phone: in the preview's actions button, which gets a dot). Press it to preview
  that commit instead, on the same runner and at the same width. The preview doesn't update by
  itself: a new checkout and setup take a while, and would clear the page or a drawing. Dash sees
  commits it merges and fetches; one pushed elsewhere shows after **History → Refresh** or the
  next automatic fetch.
- **▾ → Pick a commit…** previews any commit you pick. It stays on that commit: no **Update**.
- **▾ → Run in checkout** runs the command in the project's own checkout, uncommitted changes
  included, and shows its output on **Code → Shell** (`R` there does the same). It says **Stop the
  checkout server** while that runs.
- With no runner that can take it, no preview domain, or commit previews turned off, **Run** is
  greyed out. Hover it to see why. **Run in checkout** still works.

A web project on another computer's Dash has no commit previews: its **Run** runs the command in
its checkout, and is **Stop** while the server runs.

**Stop** ends a command with everything it started. A command that ends by itself takes its
leftovers with it too: on macOS and Linux, what it started in the background (`./server &`) gets
`SIGTERM` when it exits, and `SIGKILL` 5 seconds later if still running. A process that detaches
itself with `setsid` (most daemons) isn't reached and keeps running.

**▾** also holds the project's other actions, with their keys:

- **Run tests** (`T` on **Code → Shell**) runs the project's **Test command** in its checkout. It
  says **Testing 1:23** while it runs (click it to see the output), then **passed** or **failed**.
- **Push** (`:push`) pushes the commits waiting on the default branch, for a project that pushes to
  GitHub directly. It says how many wait.
- **Builds…** (`:ci`) opens the builds sheet and says how the latest build went. See
  [Builds](github.md#builds).

A dot on **▾** says one of them needs a look: red when the last test run or build failed, pulsing
yellow while one runs, and plain when commits wait to be pushed. Hover **▾** to see which.

A project with nothing to run (a web project with no run command) still shows **Run**, greyed out.
Hover it to see why. Click it, or **▾** then **Configure…**, to open **Project settings → Commands**
and set the command.

While it starts or runs, the button is a red **Terminate** (■), with the commit and the runner beside
it (`abc1234 on laptop`): click those to open the terminal (a web project: the preview, beside its
commit in **History**). **Terminate** stops the run at once, with no question. When the run stops by itself (idle for 30 minutes, the command exits, the runner goes
away), the button turns back into **Run** within about 20 seconds.

The project chat sits beside the terminal (on a phone: the **Chat** tab, and **Terminal** to come
back). While a session runs, the chat is told about it and can read the terminal's last lines with
the `read_try_terminal` tool. Say "note …" or describe a bug and it reports it (`report_issue`)
with the command, its output and the commit hash: that adds a draft task on the Board, tagged
`reported`. Say "make a task of this" and it creates a draft task in its own words instead.

The hub's routes are `GET|POST|DELETE /api/projects/<id>/try` (`{"runner", "ref", "kind"}` to
start; `kind` is `terminal`, the default, or `web` for a [commit preview](#preview-a-commit-from-history)),
`GET /api/projects/<id>/try/commits?runner=&q=&fetch=1`, `GET /api/projects/<id>/try/log?lines=N`
and the terminal at `/ws/projects/<id>/try`; each goes through to the runner over its tunnel.
`GET` returns `runners` (those it can run on), `candidates` (every runner with the project, each
with `online` and, when it can't run on it, `why`), `default`, `session` and `web_disabled` (why a
commit preview can't run, when it can't).

## Preview a commit from History

Open a commit in **History → Commits** to run the project as it was at that commit.

- A CLI project (**Preview tasks in** *a terminal*) has **Try at this commit**. It opens the Try
  terminal on **Code → Shell** at that commit, as **commit…** there does.
- A web project with a start server command has **Preview**. It runs the app at that commit and
  shows it beside the commit (full screen on a phone, with a button that leads back). `Esc` closes
  the preview, and `Esc` again the commit. The header's **Run** does the same for the latest
  commit (see [Run and Terminate](#run-and-terminate-the-latest-commit-in-one-click)).
- **Draw** (<kbd>P</kbd>) works on a live commit preview too. With no task to send the picture
  back to, **Attach to chat** puts it in a chat's composer instead (see [Draw](draw.md#on-a-commit-preview)).

Both run on a runner, like Try: one that is connected, has the labels the project runs on and
opens terminals for you. Without a runner for the project the buttons aren't shown. Only commits
of the default branch that origin has can run: on other commits the buttons are greyed out, and
hovering them says why. **Preview** is also greyed out without a preview domain, or with commit
previews turned off. With several runners, Dash asks which one, the first time.

A commit preview is the project's Try session, with `kind: "web"`:

1. The runner fetches origin and checks the commit out in a throwaway worktree, as Try does.
2. It copies the runner's **Files to copy into worktrees** there (`.env`, say).
3. It runs the **Setup command** (`npm ci`, say), then the **Start server command** with `$PORT`.
4. The hub serves the app at `https://<project>--<runner>.<preview domain>/`, signed in like a
   task's preview, for members of the project's organization.

A fresh worktree installs its dependencies and builds from scratch, so the first start can take a
while. Until the server answers on its port, the pane shows **Setting up and starting the server**
with the end of the log; **Server log** below it has all of it. If the setup or the server fails,
the pane says so and keeps the log open.

The rules are Try's:

- A project has one session: a commit preview or a Try terminal. Starting one at another commit
  replaces the running one, after asking.
- It stops after 30 minutes without traffic, or with **Stop** (or the header's **Terminate**). Its
  worktree is removed when it stops. **Hide preview** only hides it.
- It needs a preview domain (below).

The commit runs in a worktree, not in a sandbox. It shares the runner's dev database, its services
and the copied files. Old code may run old migrations or write data in an old format there, so
Dash warns about this before the first commit preview of a project in each browser (not for
**Run**, which runs the latest commit). To turn commit previews off for a project, clear
**Preview old commits from History** in **Project settings → Commands** (`commit_previews: false`).
That greys out the web project's **Run** too.

To start one through the API:

```sh
curl -X POST https://dash.example.com/api/projects/<id>/try \
  -H 'Content-Type: application/json' \
  -d '{"ref": "<sha>", "kind": "web"}'
```

Leave out `ref` for the tip of the default branch, as the header's **Run** does. The answer is the
session, with `url` and `open_url` (`/preview/<project>--<runner>`, which signs
the browser in). `GET /api/projects/<id>/try/log?lines=N` returns it with the end of the server's
log, and `DELETE /api/projects/<id>/try` stops it.

## Setting up a preview domain

Dash serves each preview through its own listener at `https://<task>.<preview domain>/`, as a
reverse proxy to the task's `$PORT`, WebSockets included (dev servers' hot reload). Each preview
gets a subdomain of its own instead of a path under Dash, because most apps assume they're served
from `/`. To set that up you need:

1. **A domain.** Pick a subdomain of the host you open Dash on and start Dash with
   `-preview-domain preview.dash.example.com` (or `DASH_PREVIEW_DOMAIN`). Keep previews under
   Dash's host: that makes them the same site, which lets the preview's sign-in cookie work inside
   Dash's pane.
2. **A wildcard DNS record**, `*.preview.dash.example.com`, pointing where `dash.example.com`
   points: your server's address behind Caddy, or a proxied `CNAME` to the tunnel with a
   Cloudflare Tunnel (see [Deploy the hub](deploy.md#previews-through-the-tunnel)). With
   Tailscale, point it at the machine's Tailscale address (`tailscale ip -4`) at your own DNS
   provider; `ts.net` names can't have wildcard subdomains, so `tailscale serve` alone can't serve
   previews, and records that resolve to a tailnet address keep previews private.
3. **A wildcard certificate** for `*.preview.dash.example.com`. Let's Encrypt only issues those
   through a DNS challenge (a Cloudflare Tunnel uses Cloudflare's edge certificate instead, see
   [Deploy the hub](deploy.md#previews-through-the-tunnel)). With Caddy (built with your DNS
   provider's plugin, see also [`deploy/Caddyfile`](../deploy/Caddyfile)):

   ```
   dash.example.com, *.preview.dash.example.com {
   	tls {
   		dns cloudflare {env.CF_API_TOKEN}
   	}
   	reverse_proxy 127.0.0.1:7788
   }
   ```

   Then run `./bin/dash -trust-proxy -preview-domain preview.dash.example.com`. Without a proxy,
   pass a certificate covering both names to `-tls-cert`/`-tls-key`, e.g. from
   `certbot certonly --dns-cloudflare -d dash.example.com -d '*.preview.dash.example.com'`.

To try it on one computer without DNS or certificates, open Dash at `http://dash.localhost:7788`
and start it with `-preview-domain preview.dash.localhost`. Browsers resolve every `*.localhost`
name to the computer itself.

Without a preview domain, Preview still starts the server, and the pane shows it when Dash is open
on the computer it runs on (as `http://localhost:<port>`). On other devices the pane says that
previews need a domain.

## What the proxy does

- Previews need a Dash login. A browser without one is sent to Dash to log in (an open Dash tab
  is enough), then gets a cookie for that one preview host. Dash's address for this is
  `-public-url` when set, else the address you open Dash on, but only on a host the preview domain
  is under (`dash.example.com` for `preview.dash.example.com`). Dash's own session cookie is never
  forwarded to the app, and pages served from a preview can't make changes through Dash's API.
  A cookie the app sets with a `Domain` loses it, so it stays on the preview's host. Over HTTPS
  Dash's session cookie is `__Host-dash_session`, which a preview's page can't set for Dash, and
  Dash refuses a request that carries two session cookies.
- The app sees requests as if they came to `http://localhost:<port>` (dev servers often only allow
  that host), with `X-Forwarded-Host` and `X-Forwarded-Proto` set to the preview's address.
  Redirects to `localhost:<port>` are rewritten, and `X-Frame-Options` and CSP `frame-ancestors`
  are removed so the app can be shown in Dash's pane.
- For [Draw](draw.md), it adds one script to the app's HTML pages, `/__dash/capture.js` (served by
  the proxy itself; [modern-screenshot](https://github.com/qq15725/modern-screenshot), MIT). It
  only answers Dash's page (exactly Dash's own origin, or `https://` on the host the preview domain
  is under, e.g. `https://dash.example.com` for `preview.dash.example.com`), with a picture of the page, its address, and
  which element is under the points Dash asks about. To let it run, a page's CSP gets the script's
  nonce (or `'self'`), `data:`/`blob:` images and same-origin fetches, and loses Trusted Types.
  Compressed pages are passed on uncompressed. A CSP set in a `<meta>` tag isn't changed, so such
  a page may refuse to be drawn.
- With [several computers](several-computers.md), a hub's task previews on the runner that ran it,
  and the hub serves it, through the runner's connection, at
  `<task>-<runner>.<preview domain>`; set `-preview-domain` on the hub only. See
  [Previews](several-computers.md#previews).
