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

## License

[AGPL-3.0](LICENSE)
