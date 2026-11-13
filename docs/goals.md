# Goals

A goal is something bigger than one task, such as "search answers in under 100 ms". You say
what you want done; a **coordinator** (Claude) reads the code, plans the tasks, dispatches them
and reviews what each one did. It never approves or merges a task: that stays yours.

Each goal belongs to one project and has:

- a **title** and a **description**: what you want done
- a **mode**: **ask before queuing** (the default) or **queue on its own**
- a **budget** (both optional): the most tasks it may create, and the share of the weekly usage
  window its tasks may use
- a **status**: planning, working, waiting for you, paused, done or out of budget
- a **log** of what the coordinator did, and why
- the coordinator's **conversation**: its session, which every wake-up and every message you
  send it continues

Tasks the coordinator makes carry the goal's id (`goal_id`). Their timeline says what it did to
them, e.g. "By coordinator · goal Faster search: sent this task back with notes".

## On the board

A goal's tasks are ordinary cards in the usual lanes. Each one has a chip with the goal's name,
in a colour of its own that stays the same for that goal.

- Tap a card's goal chip to show only that goal's tasks. The chip at the top of the board names
  the goal; tap it to show every task again.
- The task's timeline shows what the coordinator did in the goal's colour.
- The review count and the tab title still count every task, filtered or not.

In `dash-tui`, a task in a goal shows `◆ <goal>` under its title, and the task's detail names
the goal. To show only one goal's tasks:

- `gf` on a task filters to its goal; `gf` again shows every task.
- `:goal <name>` filters to the goal with that name (or the start of it); `:goal` shows every
  task again.

## How a goal runs

1. **Planning.** When you make a goal, the coordinator reads the code and proposes a plan:
   tasks, each with instructions, and the earlier tasks it needs merged first (they become its
   `depends_on`).
2. **Your turn (ask before queuing).** The plan waits for you: the goal is *waiting for you*.
   Accept it, edit it first, or reject it with feedback (the coordinator then plans again).
   Accepted tasks are saved as drafts; queue them from the board, or accept with `queue` to queue
   them at once. While drafts of the goal wait, so does the goal.
   **Queue on its own** skips this: the plan is queued right away.
3. **Wake-ups.** The coordinator wakes only when one of the goal's tasks enters review, fails
   or is merged, never on a timer. It reads the task's diff and log, then may send the task back
   with notes, post a short review note for its **Changes** tab, add a follow-up, or drop or
   rework planned tasks. It adds a log entry each time.
4. **The end.** The coordinator marks the goal *done* once everything is merged. A goal whose
   tasks are all made and finished, or whose tasks used their weekly share, is *out of budget*.
   Either way you are notified, and nothing wakes the coordinator after that.

In **ask before queuing** mode, follow-ups the coordinator adds are drafts for you to queue. It
can still send a task back with notes: that task was already queued by you.

If the coordinator needs a decision, it asks: the goal waits for you, with the question as its
note. Answer in the goal's chat.

## Pause, resume, edit, stop

- **Pause** holds the goal: task events wait, and a coordinator turn running now stops. Its
  tasks carry on. **Resume** goes on where it was and wakes the coordinator for what came
  meanwhile.
- **Edit** changes the title, description, mode or budget. To go on with a goal that ran out of
  budget, raise its budget, then resume it.
- **Stop** ends the goal for good: it is done, and its tasks stay on the board as they are.

## Budget

- **Max tasks** counts the tasks the coordinator created for the goal (deleted ones don't
  count). It can't create or plan more than that.
- **Weekly share** is a percentage of the weekly usage window (see weekly pacing in the
  [README](../README.md#your-first-task)). The goal's use is estimated as the window's
  utilization times its tasks' part of the cost of all the org's tasks since the window started.
  Once it reaches the share, the coordinator can't queue or send back tasks, and the goal is out
  of budget. It is only checked while the window is known: on a hub, from a runner linked to the
  project that reported its usage.

## What the coordinator can do

It runs with the chat machinery, read-only (Read, Grep and Glob) in the project's checkout, with
the `dash_board` tools of its goal (see [MCP](mcp.md#a-goals-coordinator)). It acts as the
goal's owner (in the goal's chat, as you). It can only change the goal's own tasks, only in the
goal's project. It has no tool to approve, merge, tag a release or change the checklist, and it
can't start a task despite the weekly pace.

On a hub, the coordinator runs on the owner's runner linked to the project, like a project chat.
If none is online, the events wait and the log says why; the next event, or **Resume**, tries
again.

## Talk to the coordinator

The goal's chat is connected to the coordinator's session: what you write there continues the
same conversation its wake-ups add to. Send it over the chat WebSocket (`/ws/chat`) with `"goal"`
set to the goal's id; only your latest message counts, the goal keeps the rest.
`GET /api/goals/{id}/chat` returns the conversation. `!` commands don't run in a goal's chat.

## API

| Call | What it does |
| --- | --- |
| `GET /api/goals` | the org's goals (`?project=<id>` for one project's), without their logs |
| `GET /api/projects/{id}/goals` | one project's goals |
| `POST /api/goals` | make a goal: `{"project", "title", "description", "mode", "budget": {"max_tasks", "weekly_share"}}` |
| `GET /api/goals/{id}` | a goal, with its log, its tasks and `weekly_used` |
| `PATCH /api/goals/{id}` | edit `title`, `description`, `mode` or `budget` |
| `POST /api/goals/{id}/pause` | pause it |
| `POST /api/goals/{id}/resume` | resume it |
| `POST /api/goals/{id}/stop` | stop it |
| `GET /api/goals/{id}/log` | its log |
| `POST /api/goals/{id}/plan/accept` | accept the waiting plan: `{"plan": [edited items], "queue": true}`, both optional |
| `POST /api/goals/{id}/plan/reject` | reject it: `{"feedback": "…"}` |
| `GET /api/goals/{id}/chat` | the coordinator's conversation |

`mode` is `ask` (ask before queuing) or `queue` (queue on its own). Changes to a goal are
broadcast on `/ws/events` as `goal.updated`.

```sh
curl -b cookies.txt -X POST https://dash.example.com/api/goals \
  -d '{"project":"a1b2c3d4e5f6","title":"Faster search","description":"Search answers in under 100 ms","budget":{"max_tasks":6}}'
```
