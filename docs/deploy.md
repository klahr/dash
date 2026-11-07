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

On the VPS, install Dash as usual (git, Claude Code logged in, see the [README](../README.md)) and
run it as a [service](service.md). Then:

1. **DNS.** Point `dash.example.com` at the VPS (A and/or AAAA record). For
   [live previews](previews.md), also point `*.preview.dash.example.com` at it.
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
