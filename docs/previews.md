# Live previews

When a project has a start server command (`run_command`, which gets `$PORT`), tasks in review
get a **Preview** button: it starts that command in the task's worktree and shows the app next to
the task (full screen on a phone). The board marks running previews **Live**, and **Previews (n)**
in the top bar lists them all, with Stop all. One preview runs per task. It stops when the task is
approved, sent back or merged, and after 30 minutes without traffic (`-preview-idle`). If the
server crashes, its log opens, and **Rework with this log…** hands it to Claude as rework notes.

To mark up a preview and send the picture back to Claude, see [Draw](draw.md).

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

A web project with a run command has the same button: **Run** runs the command in the project's
checkout and shows its output on **Code → Shell** (`R` there does the same). The button is
**Stop** while the server runs.

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
it (`abc1234 on laptop`): click those to open the terminal. **Terminate** stops the run at once, with
no question. When the run stops by itself (idle for 30 minutes, the command exits, the runner goes
away), the button turns back into **Run** within about 20 seconds.

The project chat sits beside the terminal (on a phone: the **Chat** tab, and **Terminal** to come
back). While a session runs, the chat is told about it and can read the terminal's last lines with
the `read_try_terminal` tool. Say "note …" or describe a bug and it reports it (`report_issue`)
with the command, its output and the commit hash: that adds a draft task on the Board, tagged
`reported`. Say "make a task of this" and it creates a draft task in its own words instead.

The hub's routes are `GET|POST|DELETE /api/projects/<id>/try` (`{"runner", "ref"}` to start),
`GET /api/projects/<id>/try/commits?runner=&q=&fetch=1`, `GET /api/projects/<id>/try/log?lines=N`
and the terminal at `/ws/projects/<id>/try`; each goes through to the runner over its tunnel.
`GET` returns `runners` (those it can run on), `candidates` (every runner with the project, each
with `online` and, when it can't run on it, `why`), `default` and `session`.

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
