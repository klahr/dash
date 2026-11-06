# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Run

Needs Go, `git`, and a logged-in [Claude Code](https://claude.com/claude-code) (`claude`).

```sh
cp .env.example .env    # set DASH_USERNAME and DASH_PASSWORD
go build -o bin/dash ./cmd/dash && ./bin/dash
```

Open http://127.0.0.1:7777, or use the terminal client:

```sh
go build -o bin/dash-tui ./cmd/dash-tui && ./bin/dash-tui
```

## Use it from your phone

Dash runs commands on your machine, so only expose it over HTTPS. Two easy ways:

- **Tailscale:** keep Dash on `127.0.0.1`, run `tailscale serve --bg 7777`, and start Dash with
  `-trust-proxy`. Open `https://<machine>.<tailnet>.ts.net` on your phone.
- **Your own certificate:** `./bin/dash -addr :7777 -tls-cert cert.pem -tls-key key.pem`.

Any other HTTPS reverse proxy (Caddy, nginx) works the same way as Tailscale: keep Dash on
`127.0.0.1` and pass `-trust-proxy`.

Dash can be installed as an app from the browser menu ("Install" or "Add to Home Screen"). It
checks for updates and offers a reload, so an installed copy never goes stale.

## Several computers

Run Dash on each computer (say home and work), each with its own projects and Claude login, and
watch them all from one dashboard. Set the same secret on every computer:

```sh
echo "DASH_PEER_TOKEN=$(openssl rand -hex 24)" >> .env   # same value everywhere
```

Make the other computers reachable over HTTPS (Tailscale is easiest, see above), then start the
one you open with a `-peer` for each of the others:

```sh
./bin/dash -name home -peer work=https://work.<tailnet>.ts.net
```

Projects are grouped by computer, and todos run on the computer that has the project.
Notifications come from all of them.

## GitHub

Connect a repository from Projects → Create GitHub repository (uses `gh`). In the project's
settings, choose what approving a todo does:

- **Merge it here:** one squashed commit on the default branch. With "Push after every merge",
  Dash pulls from GitHub first, so the push isn't rejected.
- **Open a pull request:** Dash pushes the branch and opens a PR. Failing checks and conflicts
  go back to Claude, and the todo is marked merged when the PR is merged on GitHub.

Merged todos can be reverted with one click (`u`).

## License

[AGPL-3.0](LICENSE)
