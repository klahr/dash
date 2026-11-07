# Dash

Run Claude on many tasks in parallel, each in its own git worktree. Review the diffs, then
merge them one at a time.

## Requirements

- **Go 1.27.3 or newer** (see [`go.mod`](go.mod))
- **git**
- **[Claude Code](https://claude.com/claude-code)** (`claude`), installed and logged in. Dash runs
  it as you, so run `claude` once in a terminal first and sign in.
- Optional: **[`gh`](https://cli.github.com)**, logged in, to create GitHub repositories and open
  pull requests.

## Quick start

```sh
git clone https://github.com/klahr/dash.git
cd dash
cp .env.example .env
```

Edit `.env` and set `DASH_USERNAME` and `DASH_PASSWORD` (the example's `admin` / `change-me`
are placeholders: change the password). Dash refuses to start without them, because it runs
commands on your computer. Optionally set `DASH_PROJECTS` to where new projects should be created
(the default is `~/dash-projects`; the `-projects` flag does the same).

Build and run:

```sh
go build -o bin/dash ./cmd/dash
./bin/dash
```

Open <http://127.0.0.1:7788> and sign in with the `DASH_USERNAME` and `DASH_PASSWORD` from your
`.env`. Dash listens only on `127.0.0.1:7788` by default; to change that, see `-addr` in
`./bin/dash -h` (and [phone access](docs/phone.md) before exposing it).

Prefer the terminal? There's a terminal client too:

```sh
go build -o bin/dash-tui ./cmd/dash-tui && ./bin/dash-tui
```

## Your first task

1. **Create a project.** Click **New project**. Give it a name, and either point *Existing
   repository path* at a git repository you already have, or leave it empty to create a new one.
   A *Test command* (e.g. `go test ./...`) is optional but lets Dash check Claude's work.
2. **Add a task.** Click **New task** (or press <kbd>A</kbd>). Give it a title and write
   *Instructions for Claude*, as you would in a chat.
3. **Queue it.** Click **Add to queue** (or **Save as draft** to queue it later from the
   *Drafts* column). The task moves to *Queued*, then *Running* as soon as Claude starts on it in
   its own worktree. Open the card to follow along; if Claude needs permission for something,
   Dash asks you.
4. **Review.** When Claude is done the task lands in *Review*. Open it and read the diff on the
   **Changes** tab. Not right? Click **Rework**, say what to change, and it goes back to Claude.
5. **Approve.** Click **Approve**. The task goes through *Merging* (one squashed commit on the
   default branch, or a pull request, see [GitHub](docs/github.md)) and ends up in *Done*.

Queue as many tasks as you like: they run in parallel, each in its own worktree.

## Optional setup

- **Run it as a service** so it starts at boot: [`deploy/dash.service`](deploy/dash.service), explained in
  [docs/service.md](docs/service.md).
- **Use it from your phone** over HTTPS (Tailscale Serve or your own certificate):
  [docs/phone.md](docs/phone.md).
- **Use it from Claude** with the `dash-mcp` MCP server, or as a custom connector in claude.ai
  and the Claude app (via Tailscale Funnel): [docs/mcp.md](docs/mcp.md).
- **Several computers** in one dashboard: [docs/several-computers.md](docs/several-computers.md).
- **Live previews** of each task's app, served on their own subdomains:
  [docs/previews.md](docs/previews.md).

## Reference

- [Live previews](docs/previews.md): preview domain, DNS and certificates, what the proxy does
- [Draw](docs/draw.md): mark up a preview and send the picture back to Claude
- [Scans](docs/scans.md): scheduled read-only reviews that fill a project's Issues
- [GitHub](docs/github.md): merge locally or open pull requests, auto-approve, revert
- [MCP and the claude.ai connector](docs/mcp.md)
- [Several computers](docs/several-computers.md)
- [Use Dash from your phone](docs/phone.md)
- [Run as a service](docs/service.md)

All flags: `./bin/dash -h`.

## License

[AGPL-3.0](LICENSE)
