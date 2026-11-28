# Spending caps

Spending caps stop costs from running away. A project can cap what one task may cost, and what
the whole project may spend in a day and in a month. Caps are off until you set one.

Costs are the API-equivalent cost Claude Code reports for each run, the same figure as a task's
**Token use** ([Tasks](tasks.md)) and the Wall's spend today.

## Set the caps

Open **Project settings → Spending** and fill in any of:

- **Per task**: the most one task may cost, in dollars.
- **Per day**: the most the project may spend in a day.
- **Per month**: the most the project may spend in a calendar month.

Leave a field empty for no cap of that kind. Each field saves as you change it. Days and months
start at midnight in the time zone of the browser you first set a cap in; the section says which.

## When a task reaches its cap

Dash checks a task's cost while it runs, not only at the end: it adds up the tokens Claude Code
reports for each message as the run streams. When the task's cost reaches its cap:

1. Dash stops the run.
2. The task fails with **Stopped at the $X task cap**.
3. Its cost so far still counts. A run stopped before Claude Code reported its cost counts Dash's
   estimate from its tokens, marked ≈ in **Token use**.

**Retry** on the failed task raises the cap for that task once, by another task cap on top of
what it cost so far, and queues it again. If it reaches the new cap, it stops again, and
**Retry** raises it again.

A task that has already cost its cap when it starts (because you lowered the cap) fails at once.

## When the project reaches its daily or monthly cap

Once the project has spent its daily or monthly cap:

- The queue starts none of the project's tasks. Running tasks finish.
- A banner above the board says which cap was reached and until when, with a **Spending
  settings** button. Each queued task says why it waits.
- The queue starts again at the next day (or month), or as soon as you raise the cap or clear it.

On a hub, the hub holds the queue; a task's cap goes to the runner that runs it, which stops the
run there.

## Warnings at 80%

Each cap warns once when spending reaches 80% of it: a task's cap once per task (and again if
**Retry** raised it), the daily cap once a day, the monthly cap once a month. The warning goes
through [notifications](notifications.md): a push to your browsers and the `spend.warning`
webhook event. A task's warning goes to whoever answers for the task; a project's to the org.

## What counts

- **Tasks**: every run, when it started.
- **Scans**: every scan run of the project, when it started.
- **Chats** don't count. Dash records a chat's tokens, but not its cost or model, so there is
  nothing reliable to add up.
- **Codex** runs report tokens, not cost. Dash estimates their cost from the tokens with a small
  price table (GPT-5's prices for Codex's default model), shows it with ≈ and "(estimate)", and
  counts it toward the caps. A Codex run that reports no tokens counts nothing.
- **ACP agents** ([Gemini CLI](acp-agents.md)) count the cost they report. Gemini reports none,
  so its runs count nothing.

Deleted tasks are gone, and so is what they cost.

## Spend trend

**Project settings → Spending** also shows the last 30 days:

- what the project spent today and this month, against its caps
- a bar chart of the cost per day; hover over (or tap) a bar for the day's cost
- the average cost per merged task, and each merged task's cost under **Cost per merged task**

Tasks closed without changes aren't merged tasks here.

## API and MCP

`PATCH /api/projects/{id}` takes the caps, and replaces them all:

```json
{"spending": {"task_usd": 5, "day_usd": 50, "month_usd": 500, "zone": "Europe/Stockholm"}}
```

`0` (or leaving one out) is no cap of that kind; `{"spending": {}}` turns caps off. `zone` is an
IANA time zone; empty is the time zone of the computer Dash runs on. The project has `spending`
and, while a cap holds its tasks back, `spend_hold` (`period`, `cap_usd`, `spent_usd`, `until`).

`GET /api/projects/{id}/spending` returns today's and this month's spend, the last 30 days
(`days`), the merged tasks with their cost (`merged`, `per_merged_usd`) and whether some costs are
estimates (`estimated`).

A task has `cap_usd` (its own cap after a **Retry**), `cap_hit` (the cap its last run stopped at)
and, on each run in `run_stats`, `cost_estimated`. The MCP tools `get_project` and
`update_project` read and set `spending` ([MCP](mcp.md#project-settings-and-combining-drafts)).
