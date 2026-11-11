# Deploy the hub

Dash listens on `127.0.0.1:7788` and only speaks plain HTTP there. To use it from your phone,
another computer or claude.ai, put something in front that terminates HTTPS and forwards to it,
and start Dash with `-trust-proxy` so it reads the proxy's `X-Forwarded-*` headers. Three ways:

- [**A VPS behind Caddy**](#a-vps-behind-caddy): public, with certificates that renew themselves.
  The same with [**Docker**](#run-the-hub-with-docker): the hub and Caddy in two containers,
  configured in one `.env` file.
- [**At home through a Cloudflare Tunnel**](#at-home-through-a-cloudflare-tunnel): public, with
  no open ports and no public IP needed.
- [**Tailscale**](#tailscale-private-only): private to your own devices.

The hub is the Dash you open. It runs nothing on its own computer: the work (tasks, scans, chats,
git, `gh` and the projects' commands) runs on runners, other computers that connect out to it, and
a hub needs at least one. See [Several computers](several-computers.md).

**The first two put Dash's login on the internet.** Read [The login](#the-login-is-internet-facing)
before you start. To watch it in production, see [Monitoring](#monitoring).

## A VPS behind Caddy

On the VPS, install Dash as usual (see the [README](../README.md)) and run it as a
[service](service.md). The hub needs nothing else installed: no Claude Code, no API key, no `git`
and no `gh`, since it runs nothing itself (see
[Several computers](several-computers.md)). Its work runs on [runners](several-computers.md), and
it needs at least one: `dash runner` on a computer with git and Claude Code logged in (and `gh`
for pull requests). Then:

1. **DNS.** Point `dash.example.com` at the VPS (A and/or AAAA record). For
   [live previews](previews.md), also point `*.preview.dash.example.com` at it, and for
   [org subdomains](#org-subdomains) `*.dash.example.com`.
2. **Caddy.** Install [Caddy](https://caddyserver.com/docs/install) and use
   [`deploy/Caddyfile`](../deploy/Caddyfile) with your own names. Caddy gets the certificate for
   `dash.example.com` from Let's Encrypt on its own and renews it.
3. **Firewall.** Open only 80 and 443 (and SSH). Dash's port stays closed: it listens on
   `127.0.0.1`, and `-trust-proxy` must only be used when the proxy is the only way in.
4. **Start Dash** with the public address (on the service's `ExecStart` line, or as
   `DASH_PUBLIC_URL` / `DASH_PREVIEW_DOMAIN` in its env file):

   ```sh
   ./bin/dash -trust-proxy -public-url https://dash.example.com \
              -preview-domain preview.dash.example.com
   ```

   `-public-url` turns on the [claude.ai connector](mcp.md#claudeai-and-the-claude-app-custom-connector);
   leave it out if you don't use one. Leave out `-preview-domain` if you don't use previews.

### Certificates for previews

Each task's preview gets its own host, `<task>.preview.dash.example.com`, so previews need a
wildcard certificate. Let's Encrypt only issues those through a **DNS challenge**: build Caddy with
your DNS provider's plugin (`xcaddy build --with github.com/caddy-dns/cloudflare`, or the plugin
for your provider from [caddy-dns](https://github.com/caddy-dns)) and give it an API token that
can edit the zone. The `*.preview…` block in the Caddyfile does that.

Caddy's **on-demand TLS** (one certificate per host, issued at the first request) avoids the DNS
plugin, but Caddy requires an `ask` endpoint to approve each name, which Dash doesn't have, and
every new task would cost a certificate against Let's Encrypt's rate limits. Use the DNS
challenge.

## Org subdomains

Off by default: everything is served on one host, and the org a browser works in is picked in its
account menu. With **`-org-domain dash.example.com`** (or `DASH_ORG_DOMAIN`), each organization
gets an address of its own, `acme.dash.example.com`:

- **`dash.example.com`** keeps the landing page, sign-in, signup and invitation links. Signed in,
  its front page lists your organizations and sends you to the one you pick (straight there if
  you're in only one).
- **`<slug>.dash.example.com`** is that organization's dashboard. The address decides the org; your
  membership is checked on every request. If you aren't a member, or there's no such org, you get
  the same "No such organization" page either way, with a link back to `dash.example.com`.
  Signed out, you're sent to sign in on `dash.example.com` and brought back.
- **Runners** added from an org's address are given that address: the install command and
  "Add a runner" use `https://acme.dash.example.com`.
- **The claude.ai connector** at `https://acme.dash.example.com/mcp` connects to that org without
  asking which one, and its tokens only work there (and on `dash.example.com/mcp`, as before).
- **Previews** stay on `-preview-domain`. Use `preview.dash.example.com`: the label `preview` is
  reserved, so no org can take it, and the two wildcards never meet.

### Setting it up

1. **DNS:** a wildcard record `*.dash.example.com` (A/AAAA, or a CNAME to `dash.example.com`)
   besides `dash.example.com` and `*.preview.dash.example.com`. The more specific
   `*.preview…` record keeps answering for previews.
2. **Certificates:** a wildcard certificate for `*.dash.example.com`. A wildcard covers one level
   only, so it doesn't cover `<task>.preview.dash.example.com`: previews keep their own
   `*.preview.dash.example.com` certificate. Both need the DNS challenge
   ([above](#certificates-for-previews)); [`deploy/Caddyfile`](../deploy/Caddyfile) has a block for
   each.
3. **The proxy must pass the browser's `Host`** (Caddy does by default; with nginx, set
   `proxy_set_header Host $host;`), and Dash needs `-trust-proxy` and `-public-url`:

   ```sh
   ./bin/dash -trust-proxy -public-url https://dash.example.com \
              -preview-domain preview.dash.example.com \
              -org-domain dash.example.com
   ```

   With `-trust-proxy`, Dash takes `X-Forwarded-Host` only when it is `dash.example.com` or a name
   under it.

   Behind a [Cloudflare Tunnel](#at-home-through-a-cloudflare-tunnel), add a `*.dash.example.com`
   hostname to the tunnel and a proxied `CNAME` `*.dash` record, and order an edge certificate for
   `*.dash.example.com` as for [previews](#previews-through-the-tunnel): the free one only covers
   `*.example.com`.

### Slugs

An org's subdomain is its **slug**. New orgs get one from their name (`Acme Corp` →
`acme-corp`); owners rename it under *Organization → Address*. A slug is 3 to 40 lower-case
letters, digits and hyphens, not starting or ending with a hyphen, unique, and not reserved.

- **Reserved** by default: `www`, `api`, `app`, `admin`, `preview`, `runner`, `mcp`, `oauth`,
  `auth`, `login`, `mail`, `status`, `docs`, `static`, `assets`, `help`, `support`, `billing`.
  `-org-reserved` (or `DASH_ORG_RESERVED`) replaces the list, comma-separated; the preview domain's
  first label is always reserved. `www.dash.example.com` redirects to `dash.example.com`; other
  reserved names answer "No such organization".
- **Renaming** keeps the old slug leading to the org for **30 days** (bookmarks, runners and
  connectors keep working; update them meanwhile), and no other org can take it until then.
- When you turn org subdomains on, orgs whose slug can't be a subdomain (too short, reserved) get
  a new one from their name; Dash logs each.
- An org's folder for the projects runners keep (**Add project**'s clones and local-only
  repositories, `<projects root>/<folder>/`) is fixed when the org is made and
  doesn't follow renames, so a slug given up and taken later never brings another org's checkouts
  with it.

### Sign-in across subdomains

Session cookies stay **host-only**: each host has its own, with no `Domain` attribute. A cookie
on `dash.example.com`'s parent would also be sent to `*.preview.dash.example.com`, where anyone's
preview app runs. Instead, `dash.example.com/go/<slug>` hands a signed-in browser to the org's
subdomain with a token that is signed, good for one minute and for that user, org and host only,
and works once; the subdomain turns it into a session of its own. Signing out on any host signs
out the base domain and every org subdomain that browser was handed to.

On every host, requests that change something (and WebSockets) must come from a page on the same
host (`Origin`): an org's pages can't drive another org's API, and previews can't drive any.
Pages may only be framed by their own host (`frame-ancestors 'self'`).

**Not yet:** custom domains (`dash.acme.com` for an org) would need per-domain certificates
(on-demand TLS with an approval endpoint) and a domain-ownership check; a possible follow-up.

## Run the hub with Docker

The hub only coordinates, so it fits in a small container: [`deploy/docker-compose.yml`](../deploy/docker-compose.yml)
runs it with Caddy in front, which gets and renews the certificates. Everything that differs from
one hub to the next (names, accounts, email, billing, branding) is in `deploy/.env`; the image,
the compose file and the [Caddyfile](../deploy/caddy/Caddyfile) name no domain.

**The image is the hub only.** It has no `claude`, no git, no `gh` and no project toolchains, and
needs none: like every hub it runs nothing itself and keeps no projects on its own disk.
Work, chat and titles run on **runners**, installed natively on each computer with the one-line
install from the hub's *Runners* page ([Several computers](several-computers.md)). Runners connect
out to the hub over HTTPS, so they need no open ports, public address or Docker of their own.

### Set up

1. **A server** with Docker and the compose plugin, ports **80** and **443** open (and SSH).
   Nothing else needs to be reachable: the hub's port 7788 isn't published, only Caddy reaches it,
   which is what makes `-trust-proxy` safe (the compose file says so with `-allow-unsafe=proxy-addr`,
   see [below](#the-login-is-internet-facing)).
2. **DNS records** pointing at the server (A and/or AAAA):

   | Record                         | For                                        |
   | ------------------------------ | ------------------------------------------ |
   | `dash.example.com`             | the hub (always)                           |
   | `*.preview.dash.example.com`   | [live previews](previews.md) (optional)    |
   | `*.dash.example.com`           | [org subdomains](#org-subdomains) (optional) |

3. **Configure:** get `deploy/` from the repository (or a release), then

   ```sh
   cd deploy
   cp docker.env.example .env
   ```

   and set at least `DASH_DOMAIN`, `ACME_EMAIL`, `DASH_USERNAME` and `DASH_PASSWORD` (16 or more
   random characters, e.g. `openssl rand -base64 24`: the hub refuses to start with `change-me`).
   `DASH_PUBLIC_URL` defaults to `https://$DASH_DOMAIN`. Every other setting from
   [`.env.example`](../.env.example) (SMTP, signup, Stripe, branding) goes in the same file. Pin
   `DASH_VERSION` to a release (e.g. `1.2.0`) rather than running `latest`.
4. **Start:**

   ```sh
   docker compose up -d
   docker compose logs -f      # Caddy getting its certificate, Dash starting
   ```

   Open `https://dash.example.com`, sign in, and add runners from the *Runners* page.

`docker compose ps` shows the hub's health: the image checks `dash healthcheck`, which asks the
hub's own [`/healthz`](#health), and Caddy only starts once the hub is healthy.

### Previews and org subdomains (wildcard certificates)

Without them, plain Caddy (the `caddy:2` image) is all you need: the hub's certificate comes
through the HTTP challenge. `*.preview…` and `*.dash.example.com` need **wildcard certificates**,
which Let's Encrypt only issues through a DNS challenge, so Caddy must be built with your DNS
provider's module from [caddy-dns](https://github.com/caddy-dns).
[`deploy/caddy/Dockerfile`](../deploy/caddy/Dockerfile) does that with `xcaddy`, and
[`deploy/docker-compose.dns.yml`](../deploy/docker-compose.dns.yml) uses it. In `.env`:

```sh
COMPOSE_FILE=docker-compose.yml:docker-compose.dns.yml
CADDY_DNS_PROVIDER=cloudflare          # the module's name: cloudflare, digitalocean, hetzner, ...
CADDY_DNS_API_TOKEN=...                # a token that can edit the zone
DASH_PREVIEW_DOMAIN=preview.dash.example.com
DASH_ORG_DOMAIN=dash.example.com
```

then `docker compose up -d --build`. Caddy serves a wildcard site for each domain that is set,
and only when `CADDY_DNS_PROVIDER` is set too; the hub reads the same two variables. Providers
whose module takes more than one token (e.g. `route53`) need their own `tls { dns … }` lines:
edit `wildcard` in `deploy/caddy/Caddyfile`. The token is passed to Caddy only, not to the hub.

### Upgrade

Set `DASH_VERSION` in `.env` to the new release (see [Releases](releases.md)), then

```sh
docker compose exec dash dash backup -data /data /data/backups/   # a backup to go back to
docker compose pull && docker compose up -d
```

The database migrates itself at start, after copying itself to
`/data/dash.db.pre-migration-<from>-to-<to>-<time>`. Migrations only go forward: to go back, set
the previous version and restore the backup (see [Back up and restore](#back-up-and-restore)).
What happens to running work, and the whole procedure, is in
[Upgrades, restarts and rollback](#upgrades-restarts-and-rollback).

### Back up and restore

All of the hub's state (the SQLite database, sessions, logs, uploads) is in one volume,
`dash_dash-data`, mounted at `/data`. Caddy's certificates are in `dash_caddy-data`; it gets new
ones if they are lost. The database is what matters: everything else is either rebuilt (sessions
mean signing in again) or nice to have (task logs, uploaded images).

**Scheduled backups.** [`deploy/docker.env.example`](../deploy/docker.env.example) sets
`DASH_BACKUP_DIR=/data/backups`: the hub writes a consistent copy of the database there once a
day, as `dash-<UTC time>.db`, and keeps the newest 7 (`DASH_BACKUP_KEEP`; `DASH_BACKUP_EVERY`
for another interval, e.g. `6h`). The first is written at start unless one less than a day old is
already there. They are in the same volume as the database, so copy them off the server too, e.g.
from a nightly cron job on the host:

```sh
docker compose cp dash:/data/backups/. /srv/backups/dash/
```

**A backup now**, while the hub runs, with `dash backup` in the container (it prints the file's
name):

```sh
docker compose exec dash dash backup -data /data /data/backups/
docker compose cp dash:/data/backups/dash-20260131T030000Z.db .
```

`dash backup` uses SQLite's `VACUUM INTO`, so the copy is a single consistent snapshot even while
tasks are being written, and it checks the copy before naming it. To copy the rest of the data
too (logs, uploads), stop the hub for a moment: `docker compose stop dash`,
`docker compose cp dash:/data ./dash-data-$(date +%F)`, `docker compose start dash`.

**Restore.** Stop the hub, put the backup in place as `dash.db` and remove the old database's
write-ahead log (`dash.db-wal`, `dash.db-shm`): left beside a different database, SQLite could
apply it to the restored one. The image has no shell, so do it from a throwaway container. For a
backup already in `/data/backups`:

```sh
docker compose stop dash
docker run --rm --volumes-from dash-dash-1 alpine sh -c '
  cp /data/dash.db /data/dash.db.before-restore &&
  cp /data/backups/dash-20260131T030000Z.db /data/dash.db &&
  rm -f /data/dash.db-wal /data/dash.db-shm &&
  chown 65532:65532 /data/dash.db'
docker compose start dash
```

For a backup on the host, first `docker compose cp ./dash-20260131T030000Z.db dash:/data/backups/`
(it works while the container is stopped). A backup from an older version is migrated at start;
one from a newer version is refused, so restore with the version that took it or a newer one.
`dash.db.before-restore` is the database you replaced; delete it once all is well.

Files copied into the volume with `docker compose cp` may be owned by root; Dash runs as user
65532. If it can't write its data after a restore, fix the owner:
`docker run --rm --volumes-from dash-dash-1 alpine chown -R 65532:65532 /data`.

### Build the image yourself

Release images are published to `ghcr.io/<owner>/dash` for amd64 and arm64 on every `X.Y.Z` release tag,
tagged with the version and `latest`. For a fork or local changes, build from the repository's
root and point `DASH_IMAGE`/`DASH_VERSION` at it:

```sh
docker build --build-arg VERSION=$(git describe --tags --always) -t dash:local .
```

with `DASH_IMAGE=dash` and `DASH_VERSION=local` in `deploy/.env`. Under the AGPL, a hub you
changed should point `DASH_SOURCE_URL` at your source.

## Upgrades, restarts and rollback

The hub only coordinates. The work (Claude, tests, merges, pushes) runs on runners, so restarting
the hub for a deploy or an upgrade, or after a crash, doesn't stop any of it. These rules apply
whether the hub runs as a [service](service.md) or in [Docker](#run-the-hub-with-docker).

### What a hub restart does to running work

- **Stopping.** On `SIGTERM` (`systemctl stop`, `docker compose up -d` with a new image,
  `docker stop`) or Ctrl-C, the hub logs `shutting down`, stops taking new connections, gives
  requests in flight (an approval, a runner's report) up to 5 seconds to finish, and exits with
  status 0. It needs no longer than that, well inside Docker's 10 seconds and the service's
  `TimeoutStopSec`.
- **Runners reconnect on their own.** A runner whose connection drops tries again after about 1
  second, then waits twice as long after each failure, up to 1 minute, with some jitter so they
  don't all reconnect at once. When it is back it reports every task it has for the hub,
  including tasks that finished while the hub was down (it keeps reporting finished tasks for a
  day). Restart the hub at any time; no runner needs touching.
- **Running tasks** stay `in_progress` on the hub. After it starts, the hub gives each runner
  with running tasks `-runner-grace` (2 minutes by default) to reconnect. That is longer than a
  runner's longest wait between attempts, so a restart never fails a task by itself.
- **A runner that doesn't come back** within the grace has its running tasks failed with
  "interrupted: <runner> went offline mid-task", so none look busy forever. They can be resumed:
  queue them again and they continue on that runner, with their worktree and Claude session.
  If the runner comes back on its own and says it kept working, the hub puts the task back on
  it, with nothing lost.
- **Merging tasks** (approved, being landed by their runner) work the same way. Past the grace
  they fail with "… while merging; approve it again once it is back", and never stay `merging`
  forever. A runner that finished the merge meanwhile still reports it, and the task becomes
  `merged`.
- **Approving** asks the task's runner to land it. If the runner can't be reached, the approval
  is refused and the task stays in review. If the hub dies just after the runner took the
  approval, the runner lands the task anyway and the hub hears of it when the runner reconnects.
  A merge is never landed twice or half-way: the runner pushes the squashed commit to origin
  before it counts the task merged.
- **A task sent just as the hub stopped**, which the runner took but the hub never recorded,
  belongs to that runner once it reports the task running. It isn't sent a second time.
- **Permission prompts** are held by the runner whose Claude asked. A hub restart doesn't drop
  them, and Claude keeps waiting for the answer. A runner that can't reach its hub for
  `-offline-grace` (10 minutes) stops its running tasks instead, so they don't wait forever on
  a prompt nobody can see (see [When a runner goes offline](several-computers.md#when-a-runner-goes-offline)).
- **A crash** (`kill -9`, out of memory, power loss) is handled the same way: every change to
  the database is a transaction, written completely or not at all, so the hub starts again from
  its last committed state.

### What a runner restart does

- **Stopping** (`SIGTERM`, `dash runner install` during an update, Ctrl-C): the runner signals
  its running tasks' Claude and test processes and waits up to 20 seconds for them to exit.
  Claude runs in a process group of its own, and without that wait it could keep running after
  the runner exits, beside the copy resumed at the next start. The tasks stay in progress (their
  log says "interrupted by shutdown; will resume on restart") and continue with their Claude
  session when the runner starts again. The hub keeps showing them running for up to
  `-runner-grace`.
- **A merge in hand** stays `merging` and starts again from the beginning at the next start.
  Tests cut off by the shutdown don't count as failing. A merge left unfinished in the task's
  worktree is undone first, and nothing has landed until the push to origin succeeds.
- **A revert in hand** isn't kept: the task says "Revert failed: interrupted by shutdown; revert
  it again".
- **A crash** of the runner (or its computer) may leave Claude running if only the runner
  process dies: systemd's default `KillMode` stops the whole service, Claude included. At the
  next start the runner resumes its tasks. If it stayed away past the hub's grace, the hub has
  failed those tasks in the meantime, and the runner's first report puts them back on it.

### Upgrading the hub

1. **Read the [release notes](releases.md)** of every version between yours and the new one.
2. **Take a backup:** `dash backup ~/dash-backups/` (in Docker:
   `docker compose exec dash dash backup -data /data /data/backups/`). It runs while the hub
   runs, without stopping anything.
3. **Install the new version and restart:** a new binary and `systemctl --user restart dash`, or
   a new `DASH_VERSION` and `docker compose pull && docker compose up -d`. Running tasks carry on
   as described above.
4. **Watch the log** (`journalctl --user -u dash -f` or `docker compose logs -f dash`):
   - `database backed up before migrating … backup=…/dash.db.pre-migration-<from>-to-<to>-<time>`
     when the new version changes the database. Dash copies the database beside itself before
     each migration, and doesn't start if that copy fails (e.g. a full disk).
   - `dash listening`, then `runner connected name=… version=…` as the runners come back.
5. **Update the runners** (optional, in any order): the Organization page flags runners whose
   version differs from the hub's, with the command that updates each
   ([Update a runner](several-computers.md#update-a-runner)). A runner restarted for an update
   resumes its tasks.

**Migrations only go forward.** Each one runs in a single transaction: if it fails, the database
stays exactly as it was, Dash exits with the error, and the previous version still runs on it.
Once a migration has run, an older Dash refuses to start on that database ("database schema is
version N, newer than this Dash knows") rather than misread it. There are no down-migrations.

### Rolling back

To go back to the previous version after it migrated the database:

1. Stop the hub.
2. Restore the copy taken before the migration (or your own backup from step 2) as `dash.db`, and
   delete `dash.db-wal` and `dash.db-shm`. As a service:

   ```sh
   systemctl --user stop dash
   cd ~/.local/share/dash
   mv dash.db dash.db.after-upgrade
   rm -f dash.db-wal dash.db-shm
   cp dash.db.pre-migration-10-to-11-20261109T120000Z dash.db
   ```

   In Docker, do the same in a throwaway container, as in
   [Back up and restore](#back-up-and-restore), with `/data/dash.db.pre-migration-…` as the
   file to copy.
3. Install the previous version and start it.

Anything that changed after the backup is lost with the rollback: tasks created or finished
since. Runners report the tasks they still have when they reconnect: tasks the restored database
already has catch up, but tasks created after the backup are unknown to it and stay on the
runners only. Without a migration (same schema), rolling back is just installing the previous
version: no restore needed.

The pre-migration copies are never deleted by Dash (scheduled backups don't prune them).
Delete them once the new version has proved itself.

## At home through a Cloudflare Tunnel

`cloudflared` makes an outgoing connection to Cloudflare, and Cloudflare serves your hostnames over
HTTPS and sends the traffic down that connection. Nothing listens on your router, and Cloudflare
handles the certificates. Your domain's DNS must be on Cloudflare.

1. Install [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
   and create the tunnel:

   ```sh
   cloudflared tunnel login
   cloudflared tunnel create dash
   cloudflared tunnel route dns dash dash.example.com
   ```

2. Write `~/.cloudflared/config.yml` (the tunnel id and credentials file are printed by
   `tunnel create`):

   ```yaml
   tunnel: <tunnel-id>
   credentials-file: /home/you/.cloudflared/<tunnel-id>.json
   ingress:
     - hostname: dash.example.com
       service: http://127.0.0.1:7788
     - hostname: "*.preview.dash.example.com"   # live previews; drop if unused
       service: http://127.0.0.1:7788
     - service: http_status:404
   ```

   `cloudflared` passes the original `Host` on, and Cloudflare adds `X-Forwarded-For` and
   `X-Forwarded-Proto`, which is what `-trust-proxy` needs. WebSockets (chat, terminal, previews'
   hot reload) work through it.

3. Run it as a service (`sudo cloudflared service install`, or `cloudflared tunnel run dash`) and
   start Dash:

   ```sh
   ./bin/dash -trust-proxy -public-url https://dash.example.com \
              -preview-domain preview.dash.example.com
   ```

### Previews through the tunnel

- **DNS:** add a proxied `CNAME` record `*.preview.dash` → `<tunnel-id>.cfargotunnel.com` in the
  Cloudflare dashboard (`cloudflared tunnel route dns` doesn't take wildcards).
- **Certificate:** Cloudflare's free Universal certificate covers only `example.com` and
  `*.example.com`, not `*.preview.dash.example.com`. Either order an edge certificate for
  `*.preview.dash.example.com` (Advanced Certificate Manager), or give Dash a domain of its own
  (`example.dev`) at the apex and run it with `-preview-domain example.dev`, so previews are
  `<task>.example.dev`, one level down. Don't use a preview domain that other hosts you use sit
  under: Dash treats every `<name>.<preview domain>` as a preview.
- **Cache:** Cloudflare caches `.js`, `.css` and images by URL unless told otherwise, also from
  previews that need a login. Add a Cache Rule that bypasses the cache for `dash.example.com` and
  `*.preview.dash.example.com`.

You can put [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/applications/) in
front of `dash.example.com` as a second login. If you use the claude.ai connector, exempt its paths
(`/mcp`, `/authorize`, `/token`, `/register`, `/.well-known/`): Anthropic's servers can't pass
Access.

## Tailscale (private only)

If you only use Dash from your own devices, [Tailscale](https://tailscale.com) keeps it off the
internet entirely: keep Dash on `127.0.0.1`, run `tailscale serve --bg 7788`, start Dash with
`-trust-proxy`, and open `https://<machine>.<tailnet>.ts.net` from any device on your tailnet.

- **Previews:** `ts.net` names can't have wildcard subdomains. Use a domain of your own whose
  `*.preview` record points at the machine's Tailscale address (`tailscale ip -4`), with a
  DNS-challenge certificate, see [Live previews](previews.md#setting-up-a-preview-domain).
- **claude.ai connector:** Anthropic's servers aren't on your tailnet. Tailscale Funnel can expose
  just the connector's paths, see [MCP](mcp.md#claudeai-and-the-claude-app-custom-connector).

## The login is internet-facing

Behind Caddy or a tunnel, anyone can reach Dash's login page, and Dash runs commands on your
computers. So:

- **Dash refuses to start with unsafe settings.** Started with `-public-url` or `-trust-proxy`
  (`DASH_PUBLIC_URL` counts), Dash takes itself to be internet-facing and checks:

  | Check        | Refuses when                                                                                       |
  | ------------ | -------------------------------------------------------------------------------------------------- |
  | `password`   | `DASH_PASSWORD` is unset, shorter than 16 characters, a known example (`change-me`, `password`, the docs' examples…), the username, or a few characters repeated |
  | `public-url` | `-public-url` isn't `https://` (`http://localhost` and `127.0.0.1` are fine)                        |
  | `proxy-addr` | `-trust-proxy` while `-addr` isn't a loopback address: whoever reaches the port directly can forge `X-Forwarded-For` (escaping the lockout) and `X-Forwarded-Proto` |
  | `signup`     | open signup is on without working email, so new addresses can't be confirmed                       |

  Each failure is logged as `level=ERROR msg="unsafe for an internet-facing Dash" check=…` and Dash
  exits. If you know better, for instance the hub in [Docker](#run-the-hub-with-docker) listens on
  `0.0.0.0` but only Caddy can reach its port, name the checks to skip in
  `-allow-unsafe=proxy-addr,…` (or `DASH_ALLOW_UNSAFE`; `all` skips every one). Each skipped check is then
  logged at every start as `level=WARN msg="UNSAFE: …"`.

  `DASH_PASSWORD` must be set, even after the first start, so it can be checked: it keeps the first
  account's password in step with it. Passwords people choose in the app need 8 characters.
- **Webhooks stay on the internet.** An internet-facing Dash's
  [notification webhooks](notifications.md#private-addresses) may not reach private, loopback or
  link-local addresses unless you set `-webhook-private=on`, which then logs an `UNSAFE` warning at
  every start.
- **Security headers.** Every page and API answer carries a `Content-Security-Policy` (scripts
  only from Dash itself, no inline script or `eval`, no `<object>`, framed only by Dash's own pages)
  and `X-Content-Type-Options: nosniff`. An internet-facing Dash also sends
  `Strict-Transport-Security: max-age=31536000` (its own host only, not org or preview subdomains),
  and its session cookie is always `Secure` (and `HttpOnly`, `SameSite=Strict`), even if the proxy
  forgets `X-Forwarded-Proto`. Live previews keep their app's own headers.
- **Cross-origin requests.** Requests that change something (and WebSockets) are refused when the
  browser says they come from a page on another host (`Origin`), with or without org subdomains.
  `SameSite` alone doesn't stop pages on the same site: a sibling subdomain, or a dev server on
  another port of Dash's host (ports don't count for sites). Behind a proxy, Dash compares with
  the `Host` it is given, or with `X-Forwarded-Host` under `-trust-proxy`. Calls with an API token,
  and the links in notification buttons, carry their own credential and aren't checked.

- **Use a long random password** for `DASH_PASSWORD` (`openssl rand -base64 24`), and **turn on
  two-factor sign-in** (account menu → Account → Two-factor sign-in). Org owners can require it of every
  member; see [Users and organizations](users.md#two-factor-sign-in).
- **Lockout:** after 5 failed logins from one IP address within 15 minutes, that address is locked
  out until the 15 minutes pass. Failed sign-ins through the claude.ai connector and wrong
  passwords on destructive actions count too, and so do wrong two-factor codes (which are also
  limited per account, from any address). On top of that, after 100 failed sign-ins from all
  addresses together within the window, every new sign-in is refused until it passes, so many
  addresses can't share out the guessing; signed-in browsers and connectors carry on, and
  confirming your password on a destructive action still works. An attacker can use that to keep
  others from signing in for a while, so set the global limit with your traffic in mind.
  Tune the limits with `-login-max-failures` (5), `-login-global-failures` (100; 0 turns the
  global limit off) and `-login-window` (15m), or `DASH_LOGIN_MAX_FAILURES`,
  `DASH_LOGIN_GLOBAL_FAILURES` and `DASH_LOGIN_WINDOW`.
- **Lockouts are logged** as `level=WARN msg="sign-in lockout: too many failed attempts" ip=…`
  (once per address and window; the global one without `ip`), so fail2ban or similar can ban
  the address at the firewall or proxy, e.g. with
  `failregex = msg="sign-in lockout: too many failed attempts" ip=<HOST>`.
- **`-trust-proxy` is what makes the lockout per client.** Without it every request seems to come
  from the proxy, so one guesser locks everyone out. With it, Dash takes the client from the last
  `X-Forwarded-For` entry, the one your proxy added. Only use it when the proxy is the only way to
  reach Dash.
- **Sessions** last 7 days. Changing the username or password signs out every browser and
  connector.
- For a second login, put Cloudflare Access (above) or your proxy's own authentication in front,
  keeping the connector's paths open if you use it.

## Monitoring

Three things to watch a hub with: a health endpoint for Docker, load balancers and uptime checks,
Prometheus metrics, and structured logs.

### Health

`GET /healthz` answers `200` when the hub can serve: its database answers (within 3 seconds) and
has every migration this version knows applied. Otherwise it answers `503`, naming the check that
failed; the log has the error (`msg="health check failed"`).

```json
{"status":"ok","version":"1.4.0","checks":{"database":"ok","migrations":"ok"}}
```

It needs no sign-in and answers on every host name (also org subdomains), so point any uptime
check at `https://dash.example.com/healthz`. The Docker image's `HEALTHCHECK` runs
`dash healthcheck`, which asks `http://127.0.0.1:7788/healthz`; give it another URL as its
argument if the hub listens elsewhere (`dash healthcheck http://127.0.0.1:8080/healthz`).
`/runner/version` stays what runners ask for the hub's version; it says nothing about health.

### Metrics

Metrics are off until you choose where Prometheus reads them, in the text format at `/metrics`:

- **On a listener of their own:** `-metrics-addr 127.0.0.1:9090` (or `DASH_METRICS_ADDR`) serves
  `/metrics` and `/healthz` there, without sign-in. It must be a loopback address, for a
  Prometheus or agent on the same computer, unless you also set a token. On a native install
  that's the simple way.
- **On the hub's own address, with a token:** set `DASH_METRICS_TOKEN` (16 or more characters,
  e.g. `openssl rand -hex 24`; an environment variable or `.env` only, so it isn't in the process
  list) and `/metrics` answers callers that send it as a bearer token, through Caddy like
  everything else, and `401` to everyone else. With `-metrics-addr` too, that listener asks for
  it as well, and may then listen on any address (say the container's, for a Prometheus on the
  same Docker network).

```yaml
# prometheus.yml
scrape_configs:
  - job_name: dash
    scheme: https
    authorization:
      credentials_file: /etc/prometheus/dash-token   # DASH_METRICS_TOKEN
    static_configs:
      - targets: [dash.example.com]
```

| Metric | Type | What |
| --- | --- | --- |
| `dash_runners_connected` | gauge | runners connected to the hub now |
| `dash_runners_registered` | gauge | runners registered with the hub |
| `dash_tasks{status}` | gauge | tasks in each status, in every org: `draft`, `queued`, `in_progress` (running), `review`, `merging`, `merged`, `failed` |
| `dash_tasks_failed_total` | counter | tasks that failed since the hub started |
| `dash_merge_queue_length` | gauge | approved tasks waiting in or being landed by the merge queue |
| `dash_http_requests_total{method,route,code}` | counter | requests answered, by route pattern (`/api/todos/{tid}`; `other` for static files and refused requests) |
| `dash_http_request_duration_seconds{method,route}` | histogram | time to answer them (WebSockets and runner tunnels are counted, not timed) |
| `dash_signin_lockouts_total{scope}` | counter | sign-in lockouts as they start: `ip` (one address), `global` (all addresses), `2fa` (one account's codes) |
| `dash_webhook_deliveries_total{result}` | counter | notification webhook deliveries after retries: `ok` or `failed` |
| `dash_push_deliveries_total{result}` | counter | Web Push deliveries to browsers after retries: `ok`, `failed` or `gone` (the browser dropped the subscription) |
| `dash_build_info{version}` | gauge | always 1, with the running version |
| `dash_start_time_seconds` | gauge | when the hub started (Unix time) |
| `dash_goroutines` | gauge | goroutines running now |

Some alerts to start from:

```yaml
groups:
  - name: dash
    rules:
      - alert: DashDown
        expr: up{job="dash"} == 0
        for: 5m
      - alert: DashNoRunners
        expr: dash_runners_connected == 0 and dash_tasks{status="queued"} > 0
        for: 15m
      - alert: DashTasksFailing
        expr: increase(dash_tasks_failed_total[1h]) > 5
      - alert: DashMergeQueueStuck
        expr: dash_merge_queue_length > 0 and changes(dash_merge_queue_length[1h]) == 0
        for: 1h
      - alert: DashServerErrors
        expr: sum(rate(dash_http_requests_total{code=~"5.."}[5m])) / sum(rate(dash_http_requests_total[5m])) > 0.05
        for: 10m
      - alert: DashSignInLockouts
        expr: increase(dash_signin_lockouts_total{scope="global"}[15m]) > 0
      - alert: DashWebhooksFailing
        expr: increase(dash_webhook_deliveries_total{result="failed"}[1h]) > 10
```

A runner serves the same metrics with `-metrics-addr` (its own tasks, no runner counts).

### Logs

Dash logs to standard error with Go's `slog`: one record per line, with a level, a message and
key/value attributes (`err`, `project`, `ip`, …). Two settings:

- `-log-format json` (or `DASH_LOG_FORMAT=json`): one JSON object per line, for log shippers
  (Loki/Promtail, Vector, Fluent Bit, Datadog, CloudWatch), e.g.
  `{"time":"2026-11-09T13:24:59Z","level":"WARN","msg":"sign-in lockout: too many failed attempts","ip":"203.0.113.7","failures":5,"until":"…"}`.
  The default, `text`, writes the same as `key=value` lines.
- `-log-level` (or `DASH_LOG_LEVEL`): `debug`, `info` (the default), `warn` or `error`.

With Docker the log is `docker compose logs dash`; with the [service](service.md),
`journalctl --user -u dash`. The [fail2ban filter](#the-login-is-internet-facing) above matches
the text format; with JSON, match `"msg":"sign-in lockout: too many failed attempts","ip":"<HOST>"`.
