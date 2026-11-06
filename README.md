# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Run

Needs Go, `git`, and a logged-in [Claude Code](https://claude.com/claude-code) (`claude`).

```sh
cp .env.example .env    # set DASH_USERNAME and DASH_PASSWORD
go build -o bin/dash ./cmd/dash && ./bin/dash
```

New projects are created in `~/dash-projects`; change it with `-projects` or `DASH_PROJECTS`.

Open http://127.0.0.1:7788, or use the terminal client:

```sh
go build -o bin/dash-tui ./cmd/dash-tui && ./bin/dash-tui
```

### As a service

To keep Dash running in the background and start it at boot, run it as a systemd user service.
The steps are at the top of [`deploy/dash.service`](deploy/dash.service). In short: put your
credentials in `~/.config/dash/env`, copy the unit to `~/.config/systemd/user/`, run
`loginctl enable-linger "$USER"`, then `systemctl --user enable --now dash`. Read the logs with
`journalctl --user -u dash -f`.

## Use it from Claude (MCP)

`dash-mcp` is an MCP server for any Claude client (Claude Code, Claude Desktop). It logs in to a
running Dash and lets Claude list projects, create tasks, read diffs and logs, approve or send
work back, and answer permission requests, on every computer in the dashboard.

```sh
go build -o bin/dash-mcp ./cmd/dash-mcp
claude mcp add dash -e DASH_USERNAME=me -e DASH_PASSWORD=secret -- "$PWD/bin/dash-mcp"
```

For Claude Desktop, add it to `claude_desktop_config.json`:

```json
{"mcpServers": {"dash": {"command": "/path/to/bin/dash-mcp",
  "env": {"DASH_USERNAME": "me", "DASH_PASSWORD": "secret"}}}}
```

Use `-server` (or `DASH_SERVER`) when Dash isn't on `http://127.0.0.1:7788`, and `-env` to read
the credentials from a `.env` file instead.

### claude.ai and the Claude app (custom connector)

Dash can also serve the same tools itself at `/mcp`, for a custom connector in claude.ai (and so
the Claude mobile apps). Anthropic's servers call it over the public internet, signing in with
OAuth: when you add the connector you get Dash's login page, enter your Dash username and
password, and allow access.

In the web UI, the account menu (or `:connector`) has **Connect Claude**: a step-by-step guide
with the URL to copy, a link to claude.ai's connector settings, and how many connectors are signed
in. Without `-public-url` it shows the commands below for your setup instead.

**That login is the only thing between the internet and a server that runs commands on your
computers.** Use a strong password, and expose nothing but the paths below.

With Tailscale Funnel (it serves only ports 443, 8443 and 10000; 8443 leaves 443 free for
`tailscale serve`):

```sh
for p in /mcp /authorize /token /register /.well-known; do
  tailscale funnel --bg --https=8443 --set-path=$p http://127.0.0.1:7788$p
done
./bin/dash -trust-proxy -public-url https://<machine>.<tailnet>.ts.net:8443
```

`-public-url` (or `DASH_PUBLIC_URL`) is the origin the internet reaches Dash on, exactly as
Funnel serves it, port included. It turns on `/mcp` and the OAuth endpoints; without it they
don't exist. Then in claude.ai go to Settings → Connectors → Add custom connector and enter
`https://<machine>.<tailnet>.ts.net:8443/mcp`.

- Funnel matches paths by prefix, so `/mcp/dash` and `/mcp/permissions` are reachable too. They
  only accept the short-lived tokens Dash hands its own Claude runs.
- Failed logins count towards the same lockout as the web login. Behind Funnel, `-trust-proxy`
  makes the lockout per client instead of shared by every request through the proxy.
- Access tokens last an hour, refresh tokens 30 days and rotate on every use. They are stored
  hashed in `oauth.json` in the data directory; delete it (and restart) to sign every connector
  out. Changing the username or password also signs them out.
- The connector sees every computer in the dashboard, like `dash-mcp`.

## Use it from your phone

Dash runs commands on your machine, so only expose it over HTTPS. Two easy ways:

- **Tailscale:** keep Dash on `127.0.0.1`, run `tailscale serve --bg 7788`, and start Dash with
  `-trust-proxy`. Open `https://<machine>.<tailnet>.ts.net` on your phone.
- **Your own certificate:** `./bin/dash -addr :7788 -tls-cert cert.pem -tls-key key.pem`.

Any other HTTPS reverse proxy (Caddy, nginx) works the same way as Tailscale: keep Dash on
`127.0.0.1` and pass `-trust-proxy`.

Dash can be installed as an app from the browser menu ("Install" or "Add to Home Screen"). It
checks for updates and offers a reload, so an installed copy never goes stale.

## Live previews

When a project has a start server command (`run_command`, which gets `$PORT`), tasks in review
get a **Preview** button: it starts that command in the task's worktree and shows the app next to
the task (full screen on a phone). The board marks running previews **Live**, and **Previews (n)**
in the top bar lists them all, with Stop all. One preview runs per task. It stops when the task is
approved, sent back or merged, and after 30 minutes without traffic (`-preview-idle`). If the
server crashes, its log opens, and **Send back with this log** hands it to Claude as rework notes.

Dash serves each preview through its own listener at `https://<task>.<preview domain>/`, as a
reverse proxy to the task's `$PORT`, WebSockets included (dev servers' hot reload). Each preview
gets a subdomain of its own instead of a path under Dash, because most apps assume they're served
from `/`. To set that up you need:

1. **A domain.** Pick a subdomain of the host you open Dash on and start Dash with
   `-preview-domain preview.dash.example.com` (or `DASH_PREVIEW_DOMAIN`). Keep previews under
   Dash's host: that makes them the same site, which lets the preview's sign-in cookie work inside
   Dash's pane.
2. **A wildcard DNS record**, `*.preview.dash.example.com`, pointing where `dash.example.com`
   points. Over Tailscale that's the machine's Tailscale address (`tailscale ip -4`), set at your
   own DNS provider. `ts.net` names can't have wildcard subdomains, so `tailscale serve` alone
   can't serve previews. The records only resolve to a tailnet address, so previews stay private.
3. **A wildcard certificate** for `*.preview.dash.example.com`. Let's Encrypt only issues those
   through a DNS challenge. With Caddy (built with your DNS provider's plugin):

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

What the proxy does:

- Previews need a Dash login. A browser without one is sent to Dash to log in (an open Dash tab
  is enough), then gets a cookie for that one preview host. Dash's own session cookie is never
  forwarded to the app, and pages served from a preview can't make changes through Dash's API.
- The app sees requests as if they came to `http://localhost:<port>` (dev servers often only allow
  that host), with `X-Forwarded-Host` and `X-Forwarded-Proto` set to the preview's address.
  Redirects to `localhost:<port>` are rewritten, and `X-Frame-Options` and CSP `frame-ancestors`
  are removed so the app can be shown in Dash's pane.
- With several computers, a preview runs on the computer that has the project and is served by
  that computer's Dash, so set `-preview-domain` on each one.

## Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. Set the same secret on every computer:

```sh
echo "DASH_PEER_TOKEN=$(openssl rand -hex 24)" >> .env   # same value everywhere
```

Make the other computers reachable over HTTPS (Tailscale is easiest, see above), then start the
one you open with a `-peer` for each of the others:

```sh
./bin/dash -name home -peer work=https://work.<tailnet>.ts.net
```

Projects are grouped by computer, and tasks run on the computer that has the project.
Notifications come from all of them.

## GitHub

Connect a repository from Projects → Create GitHub repository (uses `gh`). In the project's
settings, choose what approving a task does:

- **Merge it here:** one squashed commit on the default branch. With "Push after every merge",
  Dash pulls from GitHub first, so the push isn't rejected.
- **Open a pull request:** Dash pushes the branch and opens a PR. Failing checks and conflicts
  go back to Claude, and the task is marked merged when the PR is merged on GitHub.

With "Approve tasks automatically", finished tasks whose tests pass go straight to the merge queue
and the Review column is hidden; tasks with failing tests still wait there for you.

Merged tasks can be reverted with one click (`u`).

## License

[AGPL-3.0](LICENSE)
