# Live previews

When a project has a start server command (`run_command`, which gets `$PORT`), tasks in review
get a **Preview** button: it starts that command in the task's worktree and shows the app next to
the task (full screen on a phone). The board marks running previews **Live**, and **Previews (n)**
in the top bar lists them all, with Stop all. One preview runs per task. It stops when the task is
approved, sent back or merged, and after 30 minutes without traffic (`-preview-idle`). If the
server crashes, its log opens, and **Send back with this log** hands it to Claude as rework notes.

To mark up a preview and send the picture back to Claude, see [Draw](draw.md).

## Terminal previews (CLI tools)

A command-line tool never opens a port, so set **Preview tasks in** to *a terminal* in the
project's settings (`preview_kind: "terminal"`). The preview is then a terminal in the task's
worktree: the **terminal preview command** (`preview_command`) runs there, or a plain shell when
it is empty. `./.dash-bin` (git ignores it) is on `PATH` and in `$DASH_BIN`, so for example

    go build -o $DASH_BIN/tool . && exec $SHELL

leaves you at a prompt where `tool` is the task's build. Such a project needs no `run_command`.
The preview is live at once, has no URL, and is attached to over the WebSocket in its
`terminal_url` (`/ws/todos/<task>/preview`, the same protocol as other terminals; under
`/peer/<runner>/` for a runner's tasks, which open it only for the
runner's owner). It stops like a web preview, with keystrokes counting as traffic. If the command
fails or the shell exits, it shows as crashed; `GET /api/todos/<task>/preview/log?lines=N` returns
its last lines as plain text, for **Send back**.

In the web client, **Preview** on such a task opens that terminal beside the task (full screen on
a phone, with a row of Esc, Tab, Ctrl-C, Ctrl-D and arrow keys). Draw doesn't apply; instead
**Send back with transcript** puts the terminal's last 200 lines into the rework notes, or only
what you selected in the terminal if you selected something.

### Try: the project itself, at any commit

**Try** (in the sidebar, or the phone's menu, of a CLI project on a hub; `:try`) opens the same
kind of terminal on the project rather than a task, to test it without one in review. It always
runs on a runner, never on the hub: one that is connected, has the project linked and opens
terminals for you (only for the runner's owner). With one such runner it is
picked by itself; with several, pick one in the Try header; with none, Try is greyed out and says
why.

On that runner Dash fetches the default branch from origin and makes a **detached worktree** of its
clone (under `<data>/try/`), never touching the clone's own checkout, and runs `preview_command`
there with `$DASH_BIN` as above. The header shows the runner and the commit (short hash and
subject); tap the commit to pick another from the branch's history (searchable, the latest 100
matches, or type any hash, tag or branch). Picking one, or another runner, replaces the session and
its worktree. A project has one try session; opening Try again reattaches to it. It stops after 30
minutes without typing (or with **Stop**), and its worktree is removed.

The project chat sits beside the terminal (on a phone: the **Chat** tab, and **Terminal** to come
back). While a session runs, the chat is told about it and can read the terminal's last lines with
the `read_try_terminal` tool. Say "note …" or describe a bug and it files an issue (`report_issue`)
with the command, its output and the commit hash; say "make a task of this" and it creates a draft
task instead. Turn issues into tasks later from the Issues list as usual.

The hub's routes are `GET|POST|DELETE /api/projects/<id>/try` (`{"runner", "ref"}` to start),
`GET /api/projects/<id>/try/commits?runner=&q=&fetch=1`, `GET /api/projects/<id>/try/log?lines=N`
and the terminal at `/ws/projects/<id>/try`; each goes through to the runner over its tunnel.

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
  is enough), then gets a cookie for that one preview host. Dash's own session cookie is never
  forwarded to the app, and pages served from a preview can't make changes through Dash's API.
- The app sees requests as if they came to `http://localhost:<port>` (dev servers often only allow
  that host), with `X-Forwarded-Host` and `X-Forwarded-Proto` set to the preview's address.
  Redirects to `localhost:<port>` are rewritten, and `X-Frame-Options` and CSP `frame-ancestors`
  are removed so the app can be shown in Dash's pane.
- For [Draw](draw.md), it adds one script to the app's HTML pages, `/__dash/capture.js` (served by
  the proxy itself; [modern-screenshot](https://github.com/qq15725/modern-screenshot), MIT). It
  only answers Dash's page (Dash's own origin, or the host the preview domain is under, e.g.
  `dash.example.com` for `preview.dash.example.com`), with a picture of the page, its address, and
  which element is under the points Dash asks about. To let it run, a page's CSP gets the script's
  nonce (or `'self'`), `data:`/`blob:` images and same-origin fetches, and loses Trusted Types.
  Compressed pages are passed on uncompressed. A CSP set in a `<meta>` tag isn't changed, so such
  a page may refuse to be drawn.
- With [several computers](several-computers.md), a preview runs on the runner that has the
  project and the hub serves it, through the runner's connection, at
  `<task>-<runner>.<preview domain>`; set `-preview-domain` on the hub only.
