# Use Dash from your phone

Dash runs commands on your machine, so only expose it over HTTPS. Two easy ways:

- **Tailscale:** keep Dash on `127.0.0.1`, run `tailscale serve --bg 7788`, and start Dash with
  `-trust-proxy`. Open `https://<machine>.<tailnet>.ts.net` on your phone.
- **Your own certificate:** `./bin/dash -addr :7788 -tls-cert cert.pem -tls-key key.pem`.

Any other HTTPS reverse proxy (Caddy, nginx) works the same way as Tailscale: keep Dash on
`127.0.0.1` and pass `-trust-proxy`.

Dash can be installed as an app from the browser menu ("Install" or "Add to Home Screen"). It
checks for updates and offers a reload, so an installed copy never goes stale.

To use Dash from the Claude mobile apps instead, see
[the custom connector](mcp.md#claudeai-and-the-claude-app-custom-connector).
