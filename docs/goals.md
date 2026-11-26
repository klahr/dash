# Goals

A goal is something bigger than one task, such as "search answers in under 100 ms". You say
what you want done; a **coordinator** (Claude) reads the code, plans the tasks, dispatches them
and reviews what each one did. It never approves or merges a task: that stays yours.

Each goal belongs to one project and has:

- a **title** and a **description**: what you want done
- a **mode**: **Ask me before starting tasks** (the default) or **Start tasks on its own**
- a **budget** (optional): the most tasks it may create (**Max tasks**). The API can also set
  the share of the weekly usage window its tasks may use
- a **status**: planning, working, waiting for you (shown as what it waits for: **Plan to
  accept**, **Question for you** or **Drafts to queue**), paused, done, stopped or out of budget;
  a goal whose coordinator can't run shows **Can't run** instead, with the reason
- a **log** of what the coordinator did, and why
- the coordinator's **conversation**: its session, which every wake-up and every message you
  send it continues

Tasks the coordinator makes carry the goal's id (`goal_id`). Their timeline says what it did to
them, e.g. "By coordinator · goal Faster search: sent this task back with notes".

## Start a goal

Goals work on the hub's shared projects and on a Dash of your own. They don't work on a
[runner's own project](several-computers.md#projects-of-a-runners-own) in the hub's dashboard:
the runner doesn't offer goals to the hub, so **New goal…** isn't shown there. Open the project
in that computer's own Dash to use goals, or make it a shared project.

On the **Goals** page, click **New goal…**. Or start from a task:

To start a new project and its first goal from an idea, use [Write a project brief](plan-project.md)
instead.

1. Open **New task** (the **+** button) or the full task form, and pick **New goal…** (next to
   **Split…** in the form). What you typed is carried over.
2. Say what to get done. The goal is named from it.
3. Under **Tasks**, keep **Ask me before starting tasks** (the default) or pick **Start tasks on
   its own**, then click **Start goal**.

A new goal has no limit. To set one, use **Edit goal…** in the goal's **⋯** menu: **Max tasks**.
Leave it empty for no limit.

The goal opens on the **Goals** page, *planning*. In ask mode its plan shows in the goal once the
coordinator has one: each task under **Tasks**, marked *Proposed*. Click a task to read its
instructions.

- **Accept plan** makes the plan's tasks and queues them.
- **Edit plan…** opens the plan in a sheet. Edit the tasks, remove or add some, and set which
  earlier tasks each one waits for (the numbers after **After**). Then click **Accept and queue**,
  or **Accept as drafts** to queue them yourself later. **Plan again…** asks what should change
  and has the coordinator plan again.

## The Goals page

**Goals** is in the sidebar, next to **Board** and **History** (a tab in the bottom bar on a phone;
`go` on the keyboard). Each goal is a card with its status and how many of its tasks are merged
("4 of 7 merged", a waiting plan's tasks counted). Goals waiting for you, and goals that can't
run, come first. The cards fill the page's width, one column on a phone.

- **Active** and **Done** split the goals that are still going from the ones that are done or
  stopped. The tabs show once a goal is done.
- The project's name and **All projects** switch between the project's goals and the goals of
  every project, each card naming its project. They show when there is more than one project.
  Pick a goal of another project to open it: Dash switches to that project. The **Board** badge's
  "2 goals" also opens **All projects**.

Pick a goal to open it beside the list (full screen with **Back** on a phone). The list narrows
and the goal you picked stays marked in it, even once it is done. `Esc` closes it; `j` and `k` open the next and
previous goal. Its head shows the status, the mode, when it started and what its tasks spent
(tokens and cost). Below it:

- **What to get done**: the description, rendered. A long one shows its start; **Show all**
  shows the rest.
- The coordinator's question or its summary, when it has one, or why it can't run.
- **Tasks**: the goal's tasks in dependency order, each with its state (proposed, draft, queued,
  running, review, merged…). Pick one to open it. A plan waiting for you is listed last, as
  *Proposed*.
- **Budget**, only when the goal has a limit: each limit as a meter of what is used of it, and
  what its tasks spent.
- **Log**: what happened, newest first. Each entry says who did it: **You**, the
  **Coordinator** or **Dash**. A task event (a task entered review, failed or was merged) is a
  plain line, and a run of them folds into one line; click it to see them all. A task shows by
  its title: click it to open the task.

The goal's main action is the one that fits where it stands:

| The goal | Main action |
| --- | --- |
| a plan waits | **Accept plan**, with **Edit plan…** beside it |
| a question waits | **Answer**: opens the goal's chat |
| its drafts wait | **Queue drafts** |
| paused | **Resume** |
| out of budget | **Resume…** |
| can't run | **Try again** |
| planning or working | **Talk to coordinator**, with **Pause** beside it |
| done or stopped | **Talk to coordinator** |

The rest are in **⋯**: **Talk to coordinator**, **Pause**, **Edit goal…**, **Link to card…**, and
**Stop goal** (or **Delete goal** once the goal is done or stopped). The link button beside the
close button copies the goal's address, which opens it for anyone in the project (see
[Links](links.md)).

**Talk to coordinator** (and **Answer**) opens the goal's chat in the chat pane, with the
coordinator's question, plan and notes as its messages; answer there. On the **Goals** page the
chat sits beside the goal and the list, never over them. It has one goal tab, which follows the
goal you open, and it closes when you close the goal or leave **Goals**, unless the chat is
pinned. On a phone the chat is a page of its own: its **Goal** button leads back to the goal.

On a goal task's **Changes** tab, the coordinator's review note shows above the diff. It only
advises: **Approve** and **Merge** stay yours.

Goals waiting for you count in the **Board** badge, the **Goals** badge and the tab title, and
you get a notification (see [notifications](notifications.md)).

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
2. **Your turn (Ask me before starting tasks).** The plan waits for you to accept it: the goal is *waiting
   for you*. Accept it, edit it first, or reject it with feedback (the coordinator then plans
   again). **Accept plan** queues its tasks; accepted as drafts, you queue them from the board.
   While drafts of the goal wait, so does the goal.
   **Start tasks on its own** skips this: the plan is queued right away.
3. **Wake-ups.** The coordinator wakes only when one of the goal's tasks enters review, fails
   or is merged, never on a timer. It reads the task's diff and log, then may send the task back
   with notes, post a short review note for its **Changes** tab, add a follow-up, or drop or
   rework planned tasks. It adds a log entry each time.
4. **The end.** The coordinator marks the goal *done* once everything is merged. A goal whose
   tasks are all made and finished, or whose tasks used their weekly share, is *out of budget*.
   Either way you are notified, and nothing wakes the coordinator after that.

In **Ask me before starting tasks** mode, follow-ups the coordinator adds are drafts for you to queue. It
can still send a task back with notes: that task was already queued by you.

If the coordinator needs a decision, it asks: the goal waits for you, with the question as its
note. Answer in the goal's chat, or with `message_goal` from Claude (see below).

## Pause, resume, edit, stop, delete

- **Pause** holds the goal: task events wait, and a coordinator turn running now stops. Its
  tasks carry on. **Resume** goes on where it was and wakes the coordinator for what came
  meanwhile.
- **Resume…** on a goal out of budget shows its limits under the actions. Raise them and click
  **Resume**: the budget is saved and the goal resumes in one step.
- **Edit goal…** changes the name, what to get done, the mode or **Max tasks**.
- **Stop goal** ends the goal for good: it is *stopped*, and its tasks stay on the board as they
  are. The goal stays in the list. **Undo** in the toast takes it back; the goal is stopped when
  the toast goes.
- **Delete goal** (only for a goal that is done or stopped) removes the goal from the list, with
  its plan, log and the coordinator's conversation. Its tasks stay on the board as they are,
  without a goal (and without its chip). A Planning card linked to the goal shows it as removed.
  **Undo** in the toast brings it back; the goal is deleted when the toast goes.

`DELETE /api/goals/{id}` and `delete_goal` delete a goal in any state. A coordinator turn running
then stops first.

## Budget

- **Max tasks** counts the tasks the coordinator created for the goal (deleted ones don't
  count). It can't create or plan more than that.
- **Weekly share** (set with the API or Claude's `update_goal`, `budget.weekly_share`) is a
  percentage of the weekly usage window (see
  [Weekly pace](tasks.md#weekly-pace)). The goal's use is estimated as the window's
  utilization times its tasks' part of the cost of all the org's tasks since the window started.
  Once it reaches the share, the coordinator can't queue or send back tasks, and the goal is out
  of budget. It is only checked while the window is known: on a hub, from a runner that works on the
  project that reported its usage.

## What the coordinator can do

It runs with the chat machinery, read-only (Read, Grep and Glob) in the project's checkout, with
the `dash_board` tools of its goal (see [MCP](mcp.md#a-goals-coordinator)). It acts as the
goal's owner (in the goal's chat, as you). It can only change the goal's own tasks, only in the
goal's project. It has no tool to approve, merge, or tag a release, and it
can't start a task despite the weekly pace.

On a hub, the coordinator runs on the owner's runner that works on the project, like a project chat.
The hub has that runner clone the project first. If the coordinator can't run (no runner of yours
works on the project, it is offline, or the clone failed), the goal shows **Can't run** with the
reason, and you are notified. The events wait; the next event, or **Try again**, tries again.

## Talk to the coordinator

The goal's chat is connected to the coordinator's session: what you write there continues the
same conversation its wake-ups add to. Send it over the chat WebSocket (`/ws/chat`) with `"goal"`
set to the goal's id; only your latest message counts, the goal keeps the rest.
`GET /api/goals/{id}/chat` returns the conversation as the chat shows it: Dash's own messages that
wake the coordinator are left out, and what it said to you in those turns (its plan, question,
notes and summary) shows as its messages. `!` commands don't run in a goal's chat.

`POST /api/goals/{id}/message` (and the `message_goal` tool) sends a message without waiting for
the answer: the coordinator is woken with it, like by a task event, and acts on it. It answers
a question the goal waits on. A paused goal, or one out of budget, gets it when it resumes.

## From Claude

`dash-mcp`, the claude.ai connector and Dash's chats have goal tools too: `list_goals`,
`get_goal`, `create_goal`, `update_goal`, `pause_goal`, `resume_goal`, `stop_goal`,
`delete_goal`, `accept_goal_plan`, `reject_goal_plan` and `message_goal`. See
[MCP](mcp.md#goals).

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
| `DELETE /api/goals/{id}` | delete it; its tasks stay on the board, without a goal |
| `GET /api/goals/{id}/log` | its log |
| `POST /api/goals/{id}/plan/accept` | accept the waiting plan: `{"plan": [edited items], "queue": true}`, both optional |
| `POST /api/goals/{id}/plan/reject` | reject it: `{"feedback": "…"}` |
| `POST /api/goals/{id}/message` | wake the coordinator with a message: `{"text": "…"}` |
| `GET /api/goals/{id}/chat` | the coordinator's conversation |

`mode` is `ask` (ask me before starting tasks) or `queue` (start tasks on its own). Changes to a goal are
broadcast on `/ws/events` as `goal.updated`, and a deleted goal as `goal.deleted`.

```sh
curl -b cookies.txt -X POST https://dash.example.com/api/goals \
  -d '{"project":"a1b2c3d4e5f6","title":"Faster search","description":"Search answers in under 100 ms","budget":{"max_tasks":6}}'
```
