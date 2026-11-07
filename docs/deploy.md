# Deploy the hub

Dash listens on `127.0.0.1:7788` and only speaks plain HTTP there. To use it from your phone,
another computer or claude.ai, put something in front that terminates HTTPS and forwards to it,
and start Dash with `-trust-proxy` so it reads the proxy's `X-Forwarded-*` headers. Three ways:

- [**A VPS behind Caddy**](#a-vps-behind-caddy): public, with certificates that renew themselves.
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
  limited per account, from any address). The limits are fixed (`maxFailures` and
  `failureWindow` in [`internal/auth/auth.go`](../internal/auth/auth.go)), and they are per
  address, so they slow down one guesser, not many.
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
