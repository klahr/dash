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

The hub is the Dash you open. Other computers can join it as runners, which connect out to it, see
[Several computers](several-computers.md).

**The first two put Dash's login on the internet.** Read [The login](#the-login-is-internet-facing)
before you start.

## A VPS behind Caddy

On the VPS, install Dash as usual (see the [README](../README.md)) and run it as a
[service](service.md). A hub for several people, or one whose work runs on
[runners](several-computers.md), needs only `git`: no Claude Code and no API key, since it never
runs Claude itself (`-hub-claude`, see
[Claude runs on each user's own runner](several-computers.md#claude-runs-on-each-users-own-runner)).
A hub that runs tasks itself, for a single user without runners, also needs Claude Code logged in.
Then:

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
- An org's folder for new projects (`<projects root>/<folder>/`) is fixed when the org is made and
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

**The image is the hub only.** It has no `claude`, no git and no project toolchains, and runs with
`-hub-tasks off -hub-claude off`: projects hosted on the hub itself aren't supported in Docker.
Work, chat and titles run on **runners**, installed natively on each computer with the one-line
install from the hub's *Runners* page ([Several computers](several-computers.md)). Runners connect
out to the hub over HTTPS, so they need no open ports, public address or Docker of their own.

### Set up

1. **A server** with Docker and the compose plugin, ports **80** and **443** open (and SSH).
   Nothing else needs to be reachable: the hub's port 7788 isn't published, only Caddy reaches it,
   which is what makes `-trust-proxy` safe.
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

   and set at least `DASH_DOMAIN`, `ACME_EMAIL`, `DASH_USERNAME` and `DASH_PASSWORD`.
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
hub's own `/runner/version`, and Caddy only starts once the hub is healthy.

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
docker compose pull && docker compose up -d
```

The database migrates itself at start. To go back, set the previous version; take a backup
first, since a newer version's migrations may not be undone by an older one.

### Back up and restore

All of the hub's state (the SQLite database, sessions, logs, uploads) is in one volume,
`dash_dash-data`, mounted at `/data`. Caddy's certificates are in `dash_caddy-data`; it gets new
ones if they are lost.

The simplest backup stops the hub for a moment, so the database is consistent:

```sh
docker compose stop dash
docker compose cp dash:/data ./dash-backup-$(date +%F)
docker compose start dash
```

Without stopping it, take an online copy with SQLite's `.backup` from a throwaway container (the
hub's image has no shell or `sqlite3`):

```sh
docker run --rm --volumes-from dash-dash-1 -v "$PWD:/backup" alpine \
  sh -c 'apk add -q sqlite && sqlite3 /data/dash.db ".backup /backup/dash.db"'
```

To restore, stop the hub, copy the backup into the volume and start it again:

```sh
docker compose stop dash
docker compose cp ./dash-backup-2026-01-31/. dash:/data/
docker compose start dash
```

Files copied in this way may be owned by root; Dash runs as user 65532. If it can't write its
database after a restore, fix the owner:
`docker run --rm --volumes-from dash-dash-1 alpine chown -R 65532:65532 /data`.

### Build the image yourself

Release images are published to `ghcr.io/<owner>/dash` for amd64 and arm64 on every `v*` tag,
tagged with the version and `latest`. For a fork or local changes, build from the repository's
root and point `DASH_IMAGE`/`DASH_VERSION` at it:

```sh
docker build --build-arg VERSION=$(git describe --tags --always) -t dash:local .
```

with `DASH_IMAGE=dash` and `DASH_VERSION=local` in `deploy/.env`. Under the AGPL, a hub you
changed should point `DASH_SOURCE_URL` at your source.

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
  (`dashhost.dev`) at the apex and run it with `-preview-domain dashhost.dev`, so previews are
  `<task>.dashhost.dev`, one level down. Don't use a preview domain that other hosts you use sit
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
- **Peers** can be reached at their `ts.net` names.

## The login is internet-facing

Behind Caddy or a tunnel, anyone can reach Dash's login page, and Dash runs commands on your
computers. So:

- **Use a long random password** for `DASH_PASSWORD` (`openssl rand -base64 24`), and **turn on
  two-factor sign-in** (account menu → Two-factor sign-in). Org owners can require it of every
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
- **Peers** accept `DASH_PEER_TOKEN` as a bearer token; keep it at least 24 random characters
  (`openssl rand -hex 24`).
- For a second login, put Cloudflare Access (above) or your proxy's own authentication in front,
  keeping the connector's paths open if you use it.
