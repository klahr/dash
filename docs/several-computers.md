# Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. Set the same secret on every computer:

```sh
echo "DASH_PEER_TOKEN=$(openssl rand -hex 24)" >> .env   # same value everywhere
```

The token must be at least 24 characters.

Make the other computers reachable over HTTPS (Tailscale is easiest, see
[Use Dash from your phone](phone.md)), then start the one you open with a `-peer` for each of the
others:

```sh
./bin/dash -name home -peer work=https://work.<tailnet>.ts.net
```

Projects are grouped by computer, and tasks run on the computer that has the project.
Notifications come from all of them.

[Live previews](previews.md) run on the computer that has the project, so set `-preview-domain`
on each one.
