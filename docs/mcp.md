# Use Dash from Claude (MCP)

`dash-mcp` is an MCP server for any Claude client (Claude Code, Claude Desktop). It logs in to a
running Dash and lets Claude list projects, create tasks, read diffs and logs, approve or send
work back, and answer permission requests, on every computer in the dashboard.

```sh
go build -o bin/dash-mcp ./cmd/dash-mcp
claude mcp add dash -e DASH_USERNAME=me -e DASH_PASSWORD=secret -- "$PWD/bin/dash-mcp"
```

For Claude Desktop, add it to `claude_desktop_config.json`:

```json
{"mcpServers": {"dash": {"command": "/path/to/bin/dash-mcp",
  "env": {"DASH_USERNAME": "me", "DASH_PASSWORD": "secret"}}}}
```

Use `-server` (or `DASH_SERVER`) when Dash isn't on `http://127.0.0.1:7788`, and `-env` to read
the credentials from a `.env` file instead.

## claude.ai and the Claude app (custom connector)

Dash can also serve the same tools itself at `/mcp`, for a custom connector in claude.ai (and so
the Claude mobile apps). Anthropic's servers call it over the public internet, signing in with
OAuth: when you add the connector you get Dash's login page, enter your Dash username and
password, and allow access. If you belong to several organizations, the page then asks which one
the connector works in; it only ever acts in that one (see [tokens](users.md#tokens)).

In the web UI, the Account dialog (account menu → Account, or `:connector`) has **Connect Claude**: a step-by-step guide
for the people using the dashboard, with the URL to copy, a link to claude.ai's connector settings,
and how many connectors are signed in. Without `-public-url` it only says the connector is off
(and, to platform admins, which flag turns it on); setting it up is described below.

**That login is the only thing between the internet and a server that runs commands on your
computers.** Use a strong password, see [the login](deploy.md#the-login-is-internet-facing).

Behind Caddy or a Cloudflare Tunnel (see [Deploy the hub](deploy.md)), Dash is already on the
internet at its own address, so that is the public URL:

```sh
./bin/dash -trust-proxy -public-url https://dash.example.com
```

Then in claude.ai go to Settings → Connectors → Add custom connector and enter
`https://dash.example.com/mcp`.

`-public-url` (or `DASH_PUBLIC_URL`) is the origin the internet reaches Dash on, exactly as your
proxy serves it: `https://host`, with a port only if it isn't 443, and no path. It is the OAuth
issuer and the connector's resource, so it must match what claude.ai sees. It turns on `/mcp` and
the OAuth endpoints; without it they don't exist. If you put a second login (such as Cloudflare
Access) in front of Dash, leave `/mcp`, `/authorize`, `/token`, `/register` and `/.well-known/`
open to Anthropic's servers.

With [org subdomains](deploy.md#org-subdomains), connect `https://acme.dash.example.com/mcp`
instead: that address is its own issuer, sign-in there doesn't ask which organization, and the
tokens it gets only work for that org (on that address and on `https://dash.example.com/mcp`).

### Tailscale Funnel

If Dash is otherwise private on your tailnet, Funnel can expose just the connector's paths. It
serves only ports 443, 8443 and 10000; 8443 leaves 443 free for `tailscale serve`:

```sh
for p in /mcp /authorize /token /register /.well-known; do
  tailscale funnel --bg --https=8443 --set-path=$p http://127.0.0.1:7788$p
done
./bin/dash -trust-proxy -public-url https://<machine>.<tailnet>.ts.net:8443
```

and enter `https://<machine>.<tailnet>.ts.net:8443/mcp` in claude.ai.

### Notes

- `/mcp/dash` and `/mcp/permissions` are reachable too (Funnel matches paths by prefix; behind a
  proxy every path is). They only accept the short-lived tokens Dash hands its own Claude runs,
  which act as the task's (or chat's) user in its org and only reach that org's projects.
- Failed logins count towards the same lockout as the web login. Behind a proxy, `-trust-proxy`
  makes the lockout per client instead of shared by every request through the proxy.
- Access tokens last an hour, refresh tokens 30 days and rotate on every use; replaying a used
  refresh token revokes the connector. They are stored hashed in the database, as the user's
  tokens: **Account → Tokens & apps** lists the connectors you allowed and disconnects
  them. Changing your password also signs them out. (An `oauth.json` from an older Dash is imported
  once: connectors of a user in exactly one org carry on, others must connect again.)
- A client may ask for `scope=read` only; then its tools can look but not change anything.
- An [API token](users.md#tokens) works on `/mcp` as well, for MCP clients that take a bearer token.
- The connector acts as the account that connected it, in the organization chosen when it was
  allowed: it sees that org's projects and tasks only. Platform admins' connectors also see the
  other computers in the dashboard, like `dash-mcp`.

## The chat's task tools

Dash's own chats (the general chat and each project's chat) get a smaller set of task tools,
`dash_board`: `list_projects`, `list_todos` (with `status`, `tag`, `since` and `limit`; tasks merged
more than 14 days ago are left out unless you ask for `status` merged or a `since` date),
`get_todo`, `create_todo` (as a draft; `builds_on`
makes it a follow-up of a finished task, continuing that task's Claude session; on a hub, `run_on`
sends it only to that one of the project's runners),
`queue_todo` (a draft, or a failed task to retry, when you ask for it to run; with `start_now`
also a queued task, when you ask to start it now despite the weekly pace), `stop_todo` and
`restart_todo` (see below), `update_todo` (title, prompt, priority, or
`clear_priority` to unset it, dependencies, `builds_on`, `run_on` or `tags`; the prompt, `builds_on` and `run_on` only while the task is a draft, queued or failed), `delete_todo` (only
draft, queued or failed tasks, when you ask to remove one), `create_release`,
`report_issue` (see [Tags and reported problems](#tags-and-reported-problems)), the
[goal tools](#goals), the [planning tools](#planning), the [deploy tools](#deploys) and
`show_image` (see [Pictures in the chat](chat.md#pictures-in-the-chat)). A read-only chat calls them without asking, except `create_release`,
`deploy`, `rollback` and `promote`, which it can't use; a shell chat asks for the ones that change something. They act as the user chatting,
in the org they're working in.

A project's chat and a task's run reach their own project only: `list_projects` lists just that
project, and a call that names another project, or a task or goal in one, fails as if it didn't
exist. Only the general chat reaches every project in the org. A goal's coordinator works in the
goal's project only.

When a hub runs a chat or a task on a runner, these tools act on the hub's board, not the
runner's: the runner passes each call to the hub (`POST /runner/mcp/dash`, with its runner
token), and the hub decides who it acts as. A task's run acts as the task's owner, and only for a
task the hub sent that runner, in that task's project. A chat acts as the runner's owner, in the
runner's org: a project's chat only in its project, which that runner must work on (have its labels). So tasks and drafts created there show up on the hub's board,
under the hub's ids. If the hub can't be reached, the tool call fails; it never writes to the
runner's own database instead. `show_image` is the exception: it reads its file on the computer
the chat runs on, and is never passed to the hub.

In a project's chat, a draft Claude makes with `create_todo` shows as a card under its answer
once the answer ends (also when it fails or you stop it), with three buttons that act right away, without asking Claude:

- **Queue** queues it, like **Queue** in the task's detail.
- **Open** opens the task.
- **Discard** deletes the draft.

After **Queue** or **Discard** the card says **Queued ✓** or **Discarded**, and the chat notes
it ("You queued *Fix login redirect*."). Claude reads the note on your next message, so it
doesn't offer to queue that task again. A task queued, started or deleted elsewhere shows how it
is now instead of the buttons, also when you open the chat again. Asking in the chat ("queue
it") still works too.

A task keeps nothing for later tasks through these tools. Lasting facts that future tasks need
(how to build or test, a pitfall, where something lives) go in the project's `CLAUDE.md`, which git
shares with every computer.

The chat stops and reruns tasks only when you ask it to:

- `stop_todo(todo)` does what **Stop** and **Move to drafts** do on the board, for tasks on a runner too: a running
  task ends failed ("canceled"), keeping its worktree and Claude session; a queued one goes back
  to draft.
- `queue_todo(todo, fresh?, start_now?)` queues a draft or a failed task. Without `fresh`, a failed task
  continues in its worktree and Claude session, like dragging its card from Review to Queued.
  `fresh: true` starts over: its worktree (on the runner, for a hub's task), Claude session and
  earlier notes for Claude are dropped, and the next run starts from the default branch with the
  prompt alone. A task with
  an open pull request can't start over.
- With `start_now: true`, `queue_todo` also takes a task that is already queued, and starts it
  now even though the queue waits for the weekly pace, like **Start now** on the task (see
  [Start now](tasks.md#start-now)). Other tasks keep following the pace;
  a usage limit, a paused queue, unmerged dependencies or no free lane still hold the task back.
  For a project on runners, the hub sends it to a runner that waits only for the pace.
  `dash-mcp` and the connector's `queue_todo` take `start_now` too.
- `get_todo` shows `start_now` once it is set. A queued task that waits for the pace has
  `"waiting": "pace"` and `pace_reason` (say "Waiting 1h 20m to stay on pace"); `list_todos`
  marks such tasks `"waiting": "pace"` too. So the chat can tell you why a task hasn't started and
  offer to start it now.
- `restart_todo(todo, prompt?, title?, fresh?)` does it in one call for a running, queued or
  failed task: it stops the task, changes its prompt or title, and queues it. Without `fresh`, a
  new prompt reaches Claude's continued session as a note.

They follow the same rules as the board: the chat's user must belong to the task's org, and a
read-only board (an unpaid org) refuses them.

`create_release(project, version | bump, sha?, notes?)` tags a commit (default: the tip of
origin's default branch) in the project's tag format and pushes the tag, like **Tag release…** in
the History view ([Tag a release](github.md#tag-a-release)): `bump` is `patch`, `minor` or
`major` from the latest release tag, `notes` default to the tasks merged since then. Only the
org's owners and admins can. `dash-mcp` and the connector have it too, for every computer in the
dashboard, and `update_project` sets `tag_format`.

`dash-mcp` and the connector have `update_todo` too. Their `delete_todo` deletes a task in any
status except running or merging.

### Task priority

A task's priority is a number: tasks with one start first, lowest first, then the tasks without
one, oldest first. The board's **Priority** levels sit on top of that number:

| Level      | Number         | Pass                      |
| ---------- | -------------- | ------------------------- |
| **None**   | none           | `"none"` or `null`        |
| **High**   | `0` and higher | `"high"` (sets `0`)       |
| **Urgent** | below `0`      | `"urgent"` (sets `-1`)    |

`create_todo` and `update_todo` take either form, and so do
`POST /api/projects/{id}/todos` and `PATCH /api/todos/{id}`. Leave `priority` out to keep it as it is (no priority on
a new task). Tasks always come back with the number, or `null` for none. Dragging cards on the
board changes the numbers but keeps a task in its level. For example, `PATCH /api/todos/{id}`
with `{"priority": "urgent"}` makes a task urgent. An unknown level is refused with `400`.

## Tags and reported problems

Tasks have tags: short labels, at most 20, each at most 40 characters.

- `create_todo` and `update_todo` take `tags`, an array. `update_todo` replaces the list; `[]`
  clears it.
- `list_todos` takes `tag` and lists only the tasks with it. Each task comes with its `tags`, and
  a task made from reported problems also with its `severity` and `reports` (how many times they
  were reported, when more than once).
- `get_todo` returns `tags`, and `finding` for a task made from reported problems: each problem's
  fingerprint, severity, file and line, and how many times and by whom it was reported.
- `report_issue` (title, type, severity, file, line, details, fix) adds a draft task, tagged with
  its type (`bug`, `security`, `issue`), severity and `reported`. If a task that isn't merged has
  the same problem, it counts the report on that task instead. If the user dismissed it, nothing
  is added. See [Scans](scans.md#findings-on-the-board) for how Dash matches problems.

`list_issues` is gone: use `list_todos` with a `tag` instead, e.g. `security` or `scan:security`.

## A task's tools

A task's own Claude run gets these `dash_board` tools, in its project only, and calls them
without asking:

- `report_issue`: Claude reports a problem it notices outside its task, instead of fixing it on
  the task's branch. It becomes a draft task on the Board. When a task already has that problem,
  the report is added to that task instead, and its card shows the count (e.g. "×15").
- `active_work(paths?)`: what the project's other tasks are doing. It lists every other task that
  is queued, running, in review or merging, with its id, status, title, a short intent from its
  prompt, and the files it has changed so far compared with the branch it lands on (a running
  task's uncommitted changes too). With `paths`, it lists only the tasks changing those files or
  files under those folders.
- `run_tests`: runs the project's **Test command** in the task's worktree, through Dash, and
  returns the exit code and the last lines of output. The run shows in the task's log. When it
  passes and Claude changes no file afterwards, Dash doesn't run the tests again when Claude
  finishes: the log says "✔ tests already passed on <commit>; not running them again". On a
  runner, a hub's task runs it on the runner, where its worktree is.

On a hub, `active_work` covers the whole project: the hub lists the tasks on all its runners and
asks each task's runner for its files. A runner that can't be reached shows its tasks' files as
unknown. An older runner only reports the files its task has committed.

The task's system prompt tells Claude to call `active_work` before larger edits to shared files,
and, where another task already changes a file, to keep its own changes there small or mention
the overlap in its summary. When the project has a **Test command**, it also tells Claude to run
it with `run_tests` rather than in its own shell.

Each call is recorded. The calling task's log gets "👀 Checked other work: #12, #15", with links
to those tasks. The card of each task the call listed gets a "↔" link marker, and both tasks'
details list the call. See [Where tasks meet](merge-queue.md#where-tasks-meet).

## Goals

`dash-mcp`, the connector and the chats' `dash_board` manage [goals](goals.md) as you would on
the **Goals** page:

- `list_goals(project?, status?)`: goals with their status, what they wait for and how many of
  their tasks are merged. In a project chat it lists that project's; `project: "*"` lists all.
- `get_goal(goal, log?)`: the goal, the plan waiting for you, its tasks, its budget and what it
  used, and the latest `log` entries of its log (default 20).
- `create_goal(project, title?, description?, mode?, max_tasks?, weekly_share?)`: start a goal.
  `mode` is `ask` (the default) or `queue`.
- `update_goal(goal, title?, description?, mode?, max_tasks?, weekly_share?)`: only the given
  ones change.
- `pause_goal`, `resume_goal` and `stop_goal(goal)`.
- `delete_goal(goal)`: delete the goal for good. Its tasks stay on the board, without a goal.
- `accept_goal_plan(goal, queue?, tasks?)`: the waiting plan becomes drafts, or queued tasks
  with `queue`. `tasks` is your edit of the plan, as `{title, prompt, after, model}` objects.
- `reject_goal_plan(goal, feedback?)`: the coordinator plans again.
- `message_goal(goal, text)`: wake the coordinator with a message, such as the answer to its
  question. It doesn't wait for the coordinator's answer: see `get_goal`'s log.

A read-only connector (`scope=read`) can only use `list_goals` and `get_goal`. A shell chat asks
before it uses the others.

## Planning

`dash-mcp`, the connector and the chats' `dash_board` work on the org's [plan](planning.md), as
you would on the **Planning** page:

- `list_cards(board?, project?, column?, tag?)`: the cards of a board with their board, column,
  priority, target, members, owner, tags, the cards they depend on and the status of their linked
  work. `board` is a board's name or id; left out, the default board; `all` lists every board's
  cards. In a project chat, that project's cards.
- `get_card(card)`: one card, with its description, the cards that depend on it, its linked tasks
  and goals with their state, and its comments.
- `create_card(title, description?, project?, board?, column?, priority?, target?, tags?, depends_on?)`:
  add a card, on `board` (left out, the default board). `depends_on` takes the ids of cards to be
  done first, on any board. It starts no work.
- `update_card(card, title?, description?, priority?, target?, tags?, depends_on?, column?, position?, board?)`:
  only the given ones change. `column` moves the card, to the end of the column or to `position`
  (0 is the top). `board` moves it to another board, to the end of its column there. `tags` and
  `depends_on` replace the lists; `[]` clears them.
- `comment_card(card, text)`: comment on a card, as you.
- `start_card(card, kind?, project?)`: what **Start…** does. `kind` is `task` (the default,
  queued now) or `goal`; `project` defaults to the card's. It answers the card and the new work's
  id.
- `link_card(card, todo?, goal?)`: link a task or a goal to a card, so the card shows its
  status.

The plan is the org's, kept by the Dash you log in to (the hub): `dash-mcp` and the connector
read and change it there. A project's chat reaches only that project's cards and cards of no
project, links and starts only that project's work, and gives a card of no project to it when it
starts it. A read-only connector (`scope=read`) can only use `list_cards` and `get_card`. A shell
chat asks before it uses the others.

## Deploys

Dash's chats' `dash_board` reads and starts a project's [deploys](deploys.md):

- `list_deploys(project?, environment?, limit?)`: the environments, with what is live on each and
  since when, the trigger, who may deploy (`claude_may_deploy` says whether Claude may), what a
  deploy needs and what blocks one now, where `promote` goes and what `rollback` would go back to;
  and the latest deploys (default 20), newest first.
- `deploy(project?, environment, commit?)`: deploy a commit or a release tag (default: the tip of
  the default branch, as origin has it). An environment with `release_tags_only` refuses a commit
  without a release tag.
- `rollback(project?, environment)`: roll back what the environment runs now to its last good
  deploy.
- `promote(project?, from, to?)`: deploy what is live on `from` to `to` (default: `from`'s next
  environment), with no build.

Claude may deploy, roll back and promote only to an environment whose **Who may deploy** is
**Members**, whatever its user's role, and never approve a deploy: one that needs an approval waits
for a person. Freezes and requirements refuse its deploys, with the reason, like anyone's. Its
deploys show "by Claude for <user>", with the task it ran for. A shell chat asks before it uses
`deploy`, `rollback` and `promote`; a read-only chat has `list_deploys` only. A task's own run
doesn't get these tools.

## A goal's coordinator

A [goal](goals.md)'s coordinator gets its own set of `dash_board` tools, for its goal only:

- the board tools `list_todos`, `get_todo`, `create_todo`, `queue_todo`, `stop_todo`,
  `restart_todo`, `update_todo`, `delete_todo` and
  `report_issue`, in the goal's project only. Those that change a task only change the goal's
  own tasks. Tasks it creates get the goal's id and count against its budget; in "ask" mode they
  stay drafts and `queue_todo` is refused.
- `get_goal`: the goal, its tasks and the latest entries of its log
- `propose_plan(tasks, why)`: the plan, as `{title, prompt, after, model}` objects (`after`: the
  indexes of earlier tasks it needs merged first)
- `send_back(todo, notes)`: a task in review or failed goes back to Claude with notes
- `post_review_note(todo, note)`: a short note for the task's Changes tab
- `add_log(text, why, todo)`, `ask_user(question)`, `finish_goal(summary)`
- `get_diff(todo)` and `get_log(todo)`

It has no tool to approve or merge a task, tag a release or start a task
despite the weekly pace. On a runner, a coordinator turn the hub runs gets the hub goal's tools:
the runner passes the goal's id to the hub with its calls (`X-Dash-Hub-Goal`), and the hub checks
that the goal is in a project that runner works on.
