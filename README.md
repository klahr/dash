# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

Dash has two parts:

- **The hub** (`dash`): the dashboard. It signs people in and keeps the board. It runs nothing
  itself and needs no `claude`, `git` or `gh`.
- **Runners** (`dash runner`): the computers the work happens on. A runner can be the same
  computer as the hub.

## Requirements

On each runner:

- **git** (2.38 or newer finds conflicts between tasks early).
- **[Claude Code](https://claude.com/claude-code)**, logged in: run `claude` once and sign in.
  Or the **[Codex CLI](https://github.com/openai/codex)** for projects that use it
  ([Codex](docs/codex.md)).
- Optional: **[`gh`](https://cli.github.com)**, logged in, to open pull requests.

To build from source you need Go (the version in [`go.mod`](go.mod)).

## Quick start

Install a prebuilt release ([Install a release](docs/releases.md#install-a-release)), or build
from source:

```sh
git clone https://github.com/klahr/dash.git
cd dash
go build -o bin/dash ./cmd/dash
cp .env.example .env
```

Edit `.env`: set `DASH_USERNAME` and a new `DASH_PASSWORD`. They become the first account.

Start the hub:

```sh
./bin/dash
```

Open <http://127.0.0.1:7788> and sign in.

Add a runner: in the account menu choose **Runners → Add a runner** and run the command it
shows, on this computer or another one. It looks like this:

```sh
./bin/dash runner --server http://127.0.0.1:7788 --code XXXX-XXXX
```

## Use it

1. Click **Add project** and pick a GitHub repository, a remote URL, or **Local only**.
2. Click **New task** (<kbd>n</kbd>) and say what Claude should do. Click **Queue**.
3. When the task is in *Review*, read the diff. Click a line to comment on it, then **Rework…** to
   send it back, or **Approve** to merge it.

Queue as many tasks as you like: they run in parallel. See [Tasks](docs/tasks.md) for the
details: test commands, priorities, follow-up tasks, pacing and pausing.

A project has four pages in the sidebar, and Planning is the organization's:

- **Board**: the tasks. **From scans** shows the drafts scans added; **⋮ → Scans…** runs and
  sets up the scans.
- **Goals**: bigger pieces of work, each planned and reviewed by a coordinator.
- **History**: **Commits** (the default branch, with what was deployed where) and **Deploys**
  (the environments: deploy, promote, roll back).
- **Code**: **Files** (browse and edit the default branch) and **Shell** (a terminal).

## Docs

Setup:

- [Several computers](docs/several-computers.md): runners, shared projects and labels
- [Set up a team](docs/team.md)
- [Deploy the hub](docs/deploy.md): HTTPS, Docker, Cloudflare Tunnel or Tailscale
- [Run as a service](docs/service.md)
- [Users and organizations](docs/users.md): roles, two-factor sign-in, the Admin sheet
- [Sandbox Claude's runs](docs/sandbox.md)
- [Releases](docs/releases.md)

Using Dash:

- [Tasks](docs/tasks.md)
- [Goals](docs/goals.md)
- [Write a project brief](docs/plan-project.md)
- [Planning](docs/planning.md)
- [Chat](docs/chat.md)
- [Dictation](docs/dictation.md)
- [GitHub](docs/github.md) and its [webhooks](docs/github-webhooks.md)
- [Merge queue](docs/merge-queue.md)
- [Deploys](docs/deploys.md)
- [Live previews](docs/previews.md) and [Draw](docs/draw.md)
- [Scans](docs/scans.md): their findings, and problems Claude reports, become tagged drafts on the Board
- [Task boards](docs/boards.md)
- [Project color](docs/project-color.md): the top bar, and buttons and highlights
- [Status](docs/status.md): the top bar's status icon and what it lists
- [Notifications](docs/notifications.md)
- [Claude status](docs/claude-status.md)
- [Codex](docs/codex.md)
- [MCP and the claude.ai connector](docs/mcp.md)
- [Keyboard](docs/keyboard.md)
- [Links](docs/links.md)
- [Wall view](docs/wall.md)
- [Use Dash from your phone](docs/phone.md)

Running a public hub:

- [Open signup and email](docs/signup.md)
- [Billing](docs/billing.md)
- [Branding and the landing page](docs/branding.md)
- [Your data, terms and privacy](docs/privacy.md)
- [Responding to a breach](docs/breach.md)

All flags: `./bin/dash -h`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributors sign the
[Contributor License Agreement](CLA.md) once, on their first pull request.

## License

[AGPL-3.0](LICENSE)
