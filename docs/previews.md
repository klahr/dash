# Live previews

When a project has a start server command (`run_command`, which gets `$PORT`), tasks in review
get a **Preview** button: it starts that command in the task's worktree and shows the app next to
the task (full screen on a phone). The board marks running previews **Live**, and **Previews (n)**
in the top bar lists them all, with Stop all. One preview runs per task. It stops when the task is
approved, sent back or merged, and after 30 minutes without traffic (`-preview-idle`). If the
server crashes, its log opens, and **Send back with this log** hands it to Claude as rework notes.

To mark up a preview and send the picture back to Claude, see [Draw](draw.md).

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
- With [several computers](several-computers.md), a preview runs on the computer that has the
  project and is served by that computer's Dash, so set `-preview-domain` on each one.
