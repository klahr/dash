# Use Dash from your phone

Dash runs commands on your machine, so only expose it over HTTPS. Keep Dash on `127.0.0.1`,
put an HTTPS proxy in front and start Dash with `-trust-proxy`; [Deploy the hub](deploy.md) has
the setups:

- **A VPS behind Caddy**, or **at home through a Cloudflare Tunnel**: open
  `https://dash.example.com` on your phone. The login is then on the internet, see
  [the login](deploy.md#the-login-is-internet-facing).
- **Tailscale**, private to your devices: `tailscale serve --bg 7788`, then open
  `https://<machine>.<tailnet>.ts.net`.

Or without a proxy, with a certificate of your own:
`./bin/dash -addr :7788 -tls-cert cert.pem -tls-key key.pem`.

Dash can be installed as an app from the browser menu ("Install" or "Add to Home Screen"). It
checks for updates and offers a reload, so an installed copy never goes stale. Turn on
**Preferences → Notifications** in the installed app and it notifies you of reviews, failures,
approvals and failing builds while it's closed ([push notifications](notifications.md#push-notifications)).

To use Dash from the Claude mobile apps instead, see
[the custom connector](mcp.md#claudeai-and-the-claude-app-custom-connector).
