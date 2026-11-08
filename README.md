# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Requirements

- **git**
- **Go 1.27.3 or newer** (see [`go.mod`](go.mod)), only to build from source
- **[Claude Code](https://claude.com/claude-code)** (`claude`), installed and logged in, on each
  computer that runs **`dash runner`**: run `claude` once in a terminal there and sign in. The hub
  (`dash`, the dashboard) never runs Claude and needs neither `claude` nor an API key, but it
  needs **at least one runner**: all Claude work (tasks, scans, chats, titles) runs on runners,
  with their owners' logins. The runner can be the same computer as the hub
  ([several computers](docs/several-computers.md#claude-runs-on-each-users-own-runner)).
- Optional: **[`gh`](https://cli.github.com)**, logged in, to create GitHub repositories and open
  pull requests.

## Quick start

Download a prebuilt release (Linux and macOS, amd64 and arm64; no Go needed). Pick the
version and platform from the [releases page](https://github.com/klahr/dash/releases):

```sh
VERSION=1.2.0 OS=linux ARCH=amd64   # OS: linux or darwin, ARCH: amd64 or arm64
curl -fLO "https://github.com/klahr/dash/releases/download/$VERSION/dash_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fLO "https://github.com/klahr/dash/releases/download/$VERSION/checksums.txt"
sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 --ignore-missing -c checksums.txt
mkdir dash && tar -xzf "dash_${VERSION}_${OS}_${ARCH}.tar.gz" -C dash
cd dash && mkdir bin && mv dash dash-tui dash-mcp bin/
cp .env.example .env
```

Or build from source:

```sh
git clone https://github.com/klahr/dash.git
cd dash
cp .env.example .env
go build -o bin/dash ./cmd/dash
```

Either way you end up with `./bin/dash`, which the rest of this README and the docs use.

Edit `.env` and set `DASH_USERNAME` and `DASH_PASSWORD` (the example's `admin` / `change-me`
are placeholders: change the password). On first start they become the first account, and
Dash refuses to start without them, because it runs commands on your computer. Optionally set `DASH_PROJECTS` to where new projects should be created
(the default is `~/dash-projects`; the `-projects` flag does the same). Each organization's new projects
go in a folder of its own there, named after the org (`~/dash-projects/default/` at first).

Run it:

```sh
./bin/dash
```

Open <http://127.0.0.1:7788> and sign in with the `DASH_USERNAME` and `DASH_PASSWORD` from your
`.env`. That's the first account; you can invite others to it from the account menu (see
[Users and organizations](docs/users.md)). Dash listens only on `127.0.0.1:7788` by default; to change that, see `-addr` in
`./bin/dash -h` (and [Deploy the hub](docs/deploy.md) before exposing it).

Then add a runner, the computer Claude works on: the hub runs no Claude itself, and until a
runner is online the dashboard says so. In the account menu choose **Runners** → **Add a
runner** and run the command it shows, here or on another computer. On the hub's own computer,
give the runner a data folder of its own:

```sh
./bin/dash runner -data ~/.local/share/dash-runner --server http://127.0.0.1:7788 --code XXXX-XXXX
```

Link each project to a runner in its settings (**Runners**); its tasks and scans run there.

Prefer the terminal? There's a terminal client too: `./bin/dash-tui` (it's in the release
archive; from source, `go build -o bin/dash-tui ./cmd/dash-tui`).

## Your first task

1. **Create a project.** Click **New project** and give it a name to start a new repository, or
   switch to **Existing clone** to add a git repository you already have: pick one of the clones
   Dash finds in your projects folder, or give its path (the name defaults to its folder's).
   **Add existing clone** in the project menu and the command palette opens it directly.
   A clone (existing or from GitHub) that has a favicon gets it as its project icon.
   A *Test command* (e.g. `go test ./...`) is optional but lets Dash check Claude's work.
2. **Add a task.** Click **New task** (or press <kbd>n</kbd>) and write what Claude should do,
   as you would in a chat; Claude names the task. <kbd>Enter</kbd> saves it, <kbd>Shift</kbd>+<kbd>Enter</kbd>
   adds a field for another task (a pasted multi-line text stays one task). **More options…**
   (or <kbd>Shift</kbd>+<kbd>N</kbd>) opens the full form, with a title, priority, dependencies and
   attachments.
3. **Queue it.** Tick **Queue right away** (Dash remembers it), or in the full form click **Add to
   queue**; otherwise the task is saved as a draft to queue later from the *Drafts* column. The task moves to *Queued*, then *Running* as soon as Claude starts on it in
   its own worktree. Open the card to follow along; if Claude needs permission for something,
   Dash asks you.
4. **Review.** When Claude is done the task lands in *Review*. Open it and read the diff on the
   **Changes** tab. Not right? Click **Rework**, say what to change, and it goes back to Claude.
5. **Approve.** Click **Approve**. The task goes through *Merging* (one squashed commit on the
   default branch, pushed to GitHub if you like; pull requests are opt-in, see
   [GitHub](docs/github.md)) and ends up in *Done*.

Queue as many tasks as you like: they run in parallel, each in its own worktree.

## Optional setup

- **Run it as a service** so it starts at boot: [`deploy/dash.service`](deploy/dash.service), explained in
  [docs/service.md](docs/service.md).
- **Put it online** over HTTPS, on a VPS behind Caddy or at home through a Cloudflare Tunnel
  (or privately with Tailscale), to use it from your phone and other computers:
  [docs/deploy.md](docs/deploy.md). A hub whose work runs on runners also comes as a Docker image
  with a compose file: [Run the hub with Docker](docs/deploy.md#run-the-hub-with-docker).
- **Use it from Claude** with the `dash-mcp` MCP server, or as a custom connector in claude.ai
  and the Claude app: [docs/mcp.md](docs/mcp.md).
- **Several computers** in one dashboard: add each with one pasted command (`curl … /runner/install.sh | sh`), no VPN needed. Link a project to people's runners and the hub sends its tasks there, each on its owner's Claude subscription; everyone's chat, titles and clean-ups run on their own runner too, so the hub runs no Claude itself: [docs/several-computers.md](docs/several-computers.md).
- **Live previews** of each task's app, served on their own subdomains:
  [docs/previews.md](docs/previews.md).

## Reference

- [Users and organizations](docs/users.md): accounts, roles, invitations, two-factor sign-in
- [Open signup and email](docs/signup.md): self-service accounts, personal workspaces, password reset (off by default)
- [Billing](docs/billing.md): per-seat Stripe subscriptions for organizations of two or more (off by default)
- [Branding and the landing page](docs/branding.md): product name, terms and privacy links, logo, the AGPL source link, an optional landing page
- [Deploy the hub](docs/deploy.md): Caddy, Docker, Cloudflare Tunnel or Tailscale, org subdomains (`acme.dash.example.com`), and the login on the internet
- [Live previews](docs/previews.md): preview domain, DNS and certificates, what the proxy does
- [Draw](docs/draw.md): mark up a preview and send the picture back to Claude
- [Scans](docs/scans.md): scheduled read-only reviews that fill a project's Issues
- [Notification webhooks](docs/notifications.md): send reviews, failures, merges, approvals and failing builds to Slack, ntfy or a signed JSON endpoint
- [GitHub](docs/github.md): merge locally or open pull requests, work on another branch and open its pull request, auto-approve, revert, tag releases from History
- [MCP and the claude.ai connector](docs/mcp.md)
- [Several computers](docs/several-computers.md)
- [Use Dash from your phone](docs/phone.md)
- [Run as a service](docs/service.md)
- [Releases](docs/releases.md): prebuilt binaries, `dash version`, cutting a release

All flags: `./bin/dash -h`.

## Contributing

Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md) for how to build, test and
send a pull request. Contributors sign the [Contributor License Agreement](CLA.md) once, on
their first pull request; you keep the copyright in your work.

## License

[AGPL-3.0](LICENSE)
