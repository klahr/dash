# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Requirements

- **Go 1.27.3 or newer** (see [`go.mod`](go.mod)), only to build from source
- **At least one runner.** The hub (`dash`, the dashboard) runs nothing on its own computer: it
  signs people in and keeps the board, and every task, scan, chat, git command and project
  command runs on a runner (**`dash runner`**), with its owner's logins. The hub needs **no
  `claude`, `git` or `gh`** installed and no API key. The runner can be the same computer as the
  hub ([several computers](docs/several-computers.md)). Each computer that runs `dash runner` needs:
  - **git**;
  - **[Claude Code](https://claude.com/claude-code)** (`claude`), installed and logged in: run
    `claude` once in a terminal there and sign in;
  - optional: **[`gh`](https://cli.github.com)**, logged in, to create GitHub repositories and
    open pull requests. Without it, GitHub projects merge on the default branch instead of opening
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
Dash refuses to start without them: whoever signs in can have Claude and commands run on the runners.
The hub keeps no projects on its disk: where projects live is each runner's `-projects` (or
`DASH_PROJECTS` in the runner's environment; `~/dash-projects` by default).

Run it:

```sh
./bin/dash
```

Open <http://127.0.0.1:7788> and sign in with the `DASH_USERNAME` and `DASH_PASSWORD` from your
`.env`. That's the first account; you can invite others to it from the account menu (see
[Users and organizations](docs/users.md)). Dash listens only on `127.0.0.1:7788` by default; to change that, see `-addr` in
`./bin/dash -h` (and [Deploy the hub](docs/deploy.md) before exposing it).

Then add a runner, the computer the work happens on: the hub runs nothing itself (no Claude, no
git, no project commands) and keeps no projects on its own disk, and until a runner is online the
dashboard says so. In the account menu choose **Runners** → **Add a
runner** and run the command it shows, here or on another computer. On the hub's own computer
too, the runner keeps its data apart from the hub's (`~/.local/share/dash-runner`):

```sh
./bin/dash runner --server http://127.0.0.1:7788 --code XXXX-XXXX
```

Projects live on runners. A **runner's own project** is a repository on that runner's disk; a
**shared project** is a git remote linked to one or more runners, each of which clones it, and any
of them can run its tasks (**New project** on the hub: pick one of your GitHub repositories,
create one, or paste a remote URL; each runner clones it into its projects folder, or adopts a
clone of it already there; link more runners in its settings, **Runners**). See
[Two kinds of projects](docs/several-computers.md#two-kinds-of-projects). A hub that kept projects
of its own before this: see
[Moving projects off the hub](docs/several-computers.md#moving-projects-off-the-hub).

Prefer the terminal? There's a terminal client too: `./bin/dash-tui` (it's in the release
archive; from source, `go build -o bin/dash-tui ./cmd/dash-tui`).

## Your first task

1. **Create a project.** Click **New project** and pick the **Runner** it lives on (every project
   is on a runner; the hub keeps none). Then pick one of your GitHub repositories (listed with
   that runner's `gh`, filterable), a **New GitHub repo**, or a **Remote URL**: the runner clones it
   into its projects folder (or adopts a clone of it already there). For throwaway work, pick
   **Local only**: the runner starts a new repository in its projects folder (`git init` and an
   initial commit), with no remote and no GitHub; deleting that project deletes its folder too.
   Repositories already on a runner's disk are added with **Add projects from this computer…** on
   its row of the Runners page. A clone that has a favicon gets it as its project icon.
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
5. **Approve.** Click **Approve**. The task goes through *Merging* and ends up in *Done*. In a
   GitHub project Dash opens a pull request and squash-merges it once GitHub's checks pass, so
   CI runs before anything reaches the default branch; without GitHub (or if you pick **Merge
   it**) it becomes one squashed commit on the default branch, pushed if you like. See
   [GitHub](docs/github.md).

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
- **Several computers** in one dashboard: add each with one pasted command (`curl … /runner/install.sh | sh`), no VPN needed. Share a project with people's runners and the hub sends its tasks, git and commands there, each on its owner's Claude subscription; everyone's chat, titles and clean-ups run on their own runner too, so the hub runs nothing itself: [docs/several-computers.md](docs/several-computers.md).
- **Live previews** of each task's app, served on their own subdomains:
  [docs/previews.md](docs/previews.md).

## Reference

- [Chat](docs/chat.md): the general, project and task chats, and running shell commands from them with `!`
- [Users and organizations](docs/users.md): accounts, roles, invitations, two-factor sign-in
- [Open signup and email](docs/signup.md): self-service accounts, personal workspaces, password reset (off by default)
- [Billing](docs/billing.md): per-seat Stripe subscriptions for organizations of two or more (off by default)
- [Branding and the landing page](docs/branding.md): product name, terms and privacy links, logo, the AGPL source link, an optional landing page
- [Deploy the hub](docs/deploy.md): Caddy, Docker, Cloudflare Tunnel or Tailscale, org subdomains (`acme.dash.example.com`), and the login on the internet
- [Live previews](docs/previews.md): preview domain, DNS and certificates, what the proxy does
- [Draw](docs/draw.md): mark up a preview and send the picture back to Claude
- [Scans](docs/scans.md): scheduled read-only reviews that fill a project's Issues
- [Checklist](docs/checklist.md): tick off, add to and edit the repository's TODO.md (committed straight to the default branch), and move an item onto the board as a task
- [Wall view](docs/wall.md): a read-only TV dashboard of an organization's work, with a sign-in-free link for the TV
- [Task boards](docs/boards.md): keep a project's board in Dash or in an external tracker, synced both ways (the backend interface for Favro, Trello and Jira)
- [Notifications](docs/notifications.md): push notifications to your browser or installed app, even while it's closed, and webhooks that send reviews, failures, merges, approvals and failing builds to Slack, ntfy or a signed JSON endpoint
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
