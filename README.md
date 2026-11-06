# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Run

Needs Go, `git`, and a logged-in [Claude Code](https://claude.com/claude-code) (`claude`).

```sh
cp .env.example .env    # set DASH_USERNAME and DASH_PASSWORD
go build -o bin/dash ./cmd/dash && ./bin/dash
```

Open http://127.0.0.1:7777, or use the terminal client:

```sh
go build -o bin/dash-tui ./cmd/dash-tui && ./bin/dash-tui
```

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

Use `-server` (or `DASH_SERVER`) when Dash isn't on `http://127.0.0.1:7777`, and `-env` to read
the credentials from a `.env` file instead.

### claude.ai and the Claude app (custom connector)

Dash can also serve the same tools itself at `/mcp`, for a custom connector in claude.ai (and so
the Claude mobile apps). Anthropic's servers call it over the public internet, signing in with
OAuth: when you add the connector you get Dash's login page, enter your Dash username and
password, and allow access.

**That login is the only thing between the internet and a server that runs commands on your
computers.** Use a strong password, and expose nothing but the paths below.

With Tailscale Funnel (it serves only ports 443, 8443 and 10000; 8443 leaves 443 free for
`tailscale serve`):

```sh
for p in /mcp /authorize /token /register /.well-known; do
  tailscale funnel --bg --https=8443 --set-path=$p http://127.0.0.1:7777$p
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

- **Tailscale:** keep Dash on `127.0.0.1`, run `tailscale serve --bg 7777`, and start Dash with
  `-trust-proxy`. Open `https://<machine>.<tailnet>.ts.net` on your phone.
- **Your own certificate:** `./bin/dash -addr :7777 -tls-cert cert.pem -tls-key key.pem`.

Any other HTTPS reverse proxy (Caddy, nginx) works the same way as Tailscale: keep Dash on
`127.0.0.1` and pass `-trust-proxy`.

Dash can be installed as an app from the browser menu ("Install" or "Add to Home Screen"). It
checks for updates and offers a reload, so an installed copy never goes stale.

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
