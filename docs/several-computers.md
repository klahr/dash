# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. Set the same secret on every computer:

```sh
echo "DASH_PEER_TOKEN=$(openssl rand -hex 24)" >> .env   # same value everywhere
```

The token must be at least 24 characters.

Make the other computers reachable over HTTPS from the one you open (the hub), see
[Deploy the hub](deploy.md): each behind its own Caddy or Cloudflare Tunnel hostname, or on the
same tailnet. Then start the hub with a `-peer` for each of the others:

```sh
./bin/dash -name home -peer work=https://work.example.com
```

The hub calls its peers, not the other way round, so a hub on a VPS can show computers at home
through their tunnels. Give peers hostnames outside the hub's preview domain (not
`work.preview.dash.example.com`).

Projects are grouped by computer, and tasks run on the computer that has the project.
Notifications come from all of them.

[Live previews](previews.md) run on the computer that has the project, so set `-preview-domain`
on each one.
