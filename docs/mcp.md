# Use Dash from Claude (MCP)

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

## claude.ai and the Claude app (custom connector)

Dash can also serve the same tools itself at `/mcp`, for a custom connector in claude.ai (and so
the Claude mobile apps). Anthropic's servers call it over the public internet, signing in with
OAuth: when you add the connector you get Dash's login page, enter your Dash username and
password, and allow access.

In the web UI, the account menu (or `:connector`) has **Connect Claude**: a step-by-step guide
with the URL to copy, a link to claude.ai's connector settings, and how many connectors are signed
in. Without `-public-url` it shows the commands below for your setup instead.

**That login is the only thing between the internet and a server that runs commands on your
computers.** Use a strong password, see [the login](deploy.md#the-login-is-internet-facing).

Behind Caddy or a Cloudflare Tunnel (see [Deploy the hub](deploy.md)), Dash is already on the
internet at its own address, so that is the public URL:

```sh
./bin/dash -trust-proxy -public-url https://dash.example.com
```

Then in claude.ai go to Settings → Connectors → Add custom connector and enter
`https://dash.example.com/mcp`.

`-public-url` (or `DASH_PUBLIC_URL`) is the origin the internet reaches Dash on, exactly as your
proxy serves it: `https://host`, with a port only if it isn't 443, and no path. It is the OAuth
issuer and the connector's resource, so it must match what claude.ai sees. It turns on `/mcp` and
the OAuth endpoints; without it they don't exist. If you put a second login (such as Cloudflare
Access) in front of Dash, leave `/mcp`, `/authorize`, `/token`, `/register` and `/.well-known/`
open to Anthropic's servers.

### Tailscale Funnel

If Dash is otherwise private on your tailnet, Funnel can expose just the connector's paths. It
serves only ports 443, 8443 and 10000; 8443 leaves 443 free for `tailscale serve`:

```sh
for p in /mcp /authorize /token /register /.well-known; do
  tailscale funnel --bg --https=8443 --set-path=$p http://127.0.0.1:7788$p
done
./bin/dash -trust-proxy -public-url https://<machine>.<tailnet>.ts.net:8443
```

and enter `https://<machine>.<tailnet>.ts.net:8443/mcp` in claude.ai.

### Notes

- `/mcp/dash` and `/mcp/permissions` are reachable too (Funnel matches paths by prefix; behind a
  proxy every path is). They only accept the short-lived tokens Dash hands its own Claude runs.
- Failed logins count towards the same lockout as the web login. Behind a proxy, `-trust-proxy`
  makes the lockout per client instead of shared by every request through the proxy.
- Access tokens last an hour, refresh tokens 30 days and rotate on every use. They are stored
  hashed in `oauth.json` in the data directory; delete it (and restart) to sign every connector
  out. Changing the username or password also signs them out.
- The connector acts as the account that connected it, in that account's first organization:
  it sees that org's projects and tasks only. Platform admins' connectors also see the other
  computers in the dashboard, like `dash-mcp`.

## The chat's task tools

Dash's own chats (the general chat and each project's chat) get a smaller set of task tools,
`dash_board`: `list_projects`, `list_todos`, `get_todo`, `create_todo` (as a draft),
`queue_todo` (a draft, when you ask for it to run), `update_todo` (title, prompt, priority or
dependencies; the prompt only while the task is a draft, queued or failed), `delete_todo` (only
draft, queued or failed tasks, when you ask to remove one), `list_issues` and `report_issue`.
A read-only chat calls them without asking; a shell chat asks for the ones that change something.
They act as the user chatting, in the org they're working in.

`dash-mcp` and the connector have `update_todo` too. Their `delete_todo` deletes a task in any
status except running or merging.
