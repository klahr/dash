# Dash

AI-native development orchestration. Every Todo runs in its own `git worktree`, so
Claude can work on several tasks in parallel without stepping on each other. Results
go through a review queue, and approved branches land one at a time through a single
merge worker.

```
cp .env.example .env        # set DASH_USERNAME / DASH_PASSWORD
go build -o bin/dash ./cmd/dash && ./bin/dash        # web UI → http://127.0.0.1:7777
go build -o bin/dash-tui ./cmd/dash-tui && ./bin/dash-tui   # terminal UI (vim keys, ? for help)
```

Requirements: Go 1.22+, `git`, `claude` (Claude Code CLI, logged in), and `gh` for GitHub.

### Which Claude account is used

Both todos and chat go through the `claude` CLI by default, so they use whatever account
`claude` is logged into, e.g. a Pro/Max subscription. Make sure `ANTHROPIC_API_KEY` is **not** set
in Dash's environment, or Claude Code bills that key instead. `-chat api` switches chat to the
Anthropic API through the Go SDK (needs an API key).

### Frontends

* **Web** (`web/static`, embedded): mobile-first, dark. The board opens on whatever needs you
  (Review first); review diffs and approve or send back with one tap; chat; terminals via xterm.js.
* **TUI** (`cmd/dash-tui`): a client of the same API. `hjkl`, `gg`/`G`, counts, `gt`/`gT` tabs,
  `dd` delete, `:` commands (`:wq` to save and queue a todo, `:project`, `:run`, `:q`), `i`/`Esc`
  insert/normal in chat and forms. Terminals attach in place; `Ctrl-]` detaches. Credentials come from
  `.env` like the server (or a prompt); `-server` / `$DASH_SERVER` points it at a remote Dash.

## Layout

| Path | Role |
|---|---|
| `cmd/dash` | Flags and wiring: store → hub → git → engines → HTTP |
| `internal/model` | Project, Todo, Worktree, TodoStatus (+ transition table), MergeJob, Process, Event |
| `internal/store` | In-memory state with a JSON snapshot (`<data>/state.json`); emits events on every change |
| `internal/gitops` | `git`/`gh` via `os/exec`; every command is broadcast as a `git.op` event |
| `internal/queue` | `TodoEngine` (priority dispatch, N parallel workers) and `MergeQueue` (`chan MergeJob` + one goroutine) |
| `internal/agent` | `Executor` + `ClaudeCode` (headless `claude -p` in the worktree); chat via `ClaudeCodeChat` (default) or `Chat` (Messages API) |
| `internal/runner` | Web servers / tasks (own process group via `Setpgid`), PTYs (`creack/pty`), scrollback, attach |
| `internal/auth` | Single-account login from `.env`, cookie sessions, login rate limiting |
| `internal/hub` | WebSocket fan-out of events to dashboards |
| `internal/api` | REST routes (Go 1.22 `ServeMux`) and WebSocket endpoints |
| `internal/tui`, `cmd/dash-tui` | Bubble Tea terminal client with vim keybindings |
| `web/static` | Vanilla JS frontend (embedded into the binary), xterm.js |

## Todo lifecycle

```
draft → queued → in_progress → review → merging → merged
           ▲          │           │  │       │
           └─ rework ─┴── failed ◀┘  │       └─ conflict → failed
                                     └─ rework (feedback appended, worktree reused)
```

* **Execute**: the dispatcher picks the lowest `priority` queued todo, creates
  `worktrees/<project>/todo-<id>` on branch `dash/todo-<id>`, runs Claude there, then commits.
* **Merge**: in the todo's worktree, rebase onto the default branch (a conflict aborts and fails
  the job, leaving `main` untouched). Then fast-forward `main` and remove the worktree. Optionally push.

## API

All routes except `/login.html` and `POST /api/login` need a session cookie (401 for API/WS, redirect for pages).

| Method | Path | |
|---|---|---|
| POST | `/api/login` · `/api/logout` | `{"username","password"}`; sets / clears the `dash_session` cookie |
| GET/POST | `/api/projects` | list / create (`name`, optional `path` to adopt a repo, `run_command`, `test_command`, `deploy_command`) |
| GET/PATCH | `/api/projects/{pid}` | |
| POST | `/api/projects/{pid}/github` | `gh repo create` + push (`name`, `private`) |
| POST | `/api/projects/{pid}/push` | push default branch |
| GET/POST | `/api/projects/{pid}/todos` | `title`, `prompt`, `goal`, `priority`, `enqueue` |
| GET | `/api/projects/{pid}/queues` | todo / in_progress / review / merge |
| GET/PATCH/DELETE | `/api/todos/{tid}` | |
| POST | `/api/todos/{tid}/enqueue` · `cancel` · `approve` · `rework` | `rework` takes `feedback` |
| GET | `/api/todos/{tid}/diff` · `log` | text/plain |
| GET/POST | `/api/projects/{pid}/processes` | `kind` web\|task\|pty, `preset` run\|test\|deploy or `command`, optional `todo_id` to run inside that worktree |
| DELETE | `/api/processes/{id}` | SIGTERM the process group, SIGKILL after 5 s |
| WS | `/ws/events[?project=]` | `todo.updated`, `todo.output`, `git.op`, `merge.queue`, `process.updated`, … |
| WS | `/ws/chat` | send `{"type":"send","messages":[…]}`; receive `{"kind":"thinking"\|"text"\|"done"\|"error"}` |
| WS | `/ws/proc/{id}` | binary terminal output; send `{"type":"input"}` / `{"type":"resize"}` |

## Notes

* **Login**: one account, from `DASH_USERNAME` / `DASH_PASSWORD` in `.env` (path via `-env`; real environment
  variables win). Dash refuses to start without them. Sessions live in memory for 7 days, so a restart logs you
  out. After 5 failed logins from one IP that IP is locked out for 15 minutes. The cookie is `HttpOnly` +
  `SameSite=Strict`, and WebSockets only accept same-host origins.
* Dash executes commands. Even with login, put it behind HTTPS before exposing it beyond localhost.
* Commits made by Dash are unsigned by default (`-sign-commits` to keep your signing config). A headless
  server can't answer a pinentry prompt.
* `-claude-args` controls what headless Claude Code may do (default `--permission-mode acceptEdits`).
  Add `--allowedTools "Bash(npm test:*)"` etc. to allow more.
