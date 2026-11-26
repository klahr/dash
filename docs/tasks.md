# Tasks

How a task goes from an idea to a commit on the default branch, and how to control when tasks run.

Tasks run on runners. A member's tasks run only on their own runner or a shared one. If you have
none, add one in **Runners → Add a runner**, or ask an admin for a shared one.

## Add a project

Click **Add project**. With several runners, pick one beside the title. Then pick one of:

- **From GitHub**: your repositories, listed with that runner's `gh`, filterable.
- **New GitHub repository**.
- **From URL**: the runner clones it into its projects folder, or adopts a clone of it already
  there. The dialog shows each step until the clone is done.
- **Local only**: for throwaway work. The runner starts a new repository in its projects folder
  (`git init` and an initial commit), with no remote and no GitHub. Deleting the project deletes
  its folder too.
- **Write a project brief**: Claude interviews you about an idea, writes a brief, and Dash starts
  the project and a goal from it ([Write a project brief](plan-project.md)).

Repositories already on a runner's disk are added with **Add from this runner…** on its page in
**Runners**. A clone that has a favicon gets it as its project icon.

A *Test command* (e.g. `go test ./...`) is optional but lets Dash check Claude's work. It runs
when Claude finishes, and again in the task's worktree before Dash merges it, unless it already
passed on exactly that code. Claude runs it through Dash (the `run_tests` tool), so tests that
passed in Claude's session aren't run a second time. A task whose tests fail there goes back to
Claude instead of onto the default branch. Make it run what your CI runs (formatting, linters,
tests), so a red build never reaches the default branch.

A run of the Test command that takes more than 20 minutes is stopped and counts as failed. If your
tests take longer, for example on a runner busy with many tasks at once, raise **Project settings →
Commands → Test time limit**. On runners, the hub's setting applies.

## Add a task

Click **New task** (or press <kbd>n</kbd>) and write what Claude should do, as you would in a
chat; Claude names the task. The sheet's title names the project it goes into, e.g. "New task in
**dash**". <kbd>Shift</kbd>+<kbd>Enter</kbd> starts a new line in the task.
<kbd>Alt</kbd>+<kbd>Enter</kbd> (<kbd>Option</kbd>+<kbd>Enter</kbd> on a Mac) or **Another task** adds a
field for another task.

Closing the sheet keeps what you typed on this device, and it is back the next time you open it.
Saving the tasks clears it.

Too big for a task? **Something bigger?** has **New goal…** (see [Goals](goals.md)) and **Write a
feature brief** (see [Write a feature brief](plan-project.md#write-a-feature-brief)). What you
typed is carried over.

Type `#tag` to tag the task. Dash takes the tag out of the text and shows it below the field:

```text
Fix login redirect #auth #bug
```

A `#` starts a tag only at the start of a word, and the tag needs a letter. Letters, digits, `-`
and `_` make up a tag, with `.`, `/` or `:` allowed inside it (`#v1.2`, `#scan:deps`). So
`issue #123`, `C#`, `page#top` and `# heading` stay as you typed them, and so does a `#` in
`code`.

**More options…** (or <kbd>Shift</kbd>+<kbd>N</kbd>) opens the full form: the instructions first,
then attachments, a title (empty: Claude names the task), priority, tags, dependencies and the task
it builds on. The model is under **Advanced**. **Something bigger?** is there too.

- **Clear** empties the form and deletes its draft. Click **Undo** in the message that follows to
  get it all back, attachments and tags included.
- **Priority** is **None**, **High** or **Urgent**: urgent tasks start first, then high ones, then
  the rest, oldest first. The API takes the same levels; see [Task priority](mcp.md#task-priority).
  The card shows a priority and a model as chips beside its tags.
- **Split…** has Claude propose smaller tasks for a large one, each reviewed and merged on its
  own. Edit, remove or add to them, then save them all, each one waiting for the tasks it builds
  on. Split from a saved task, the new tasks replace it: tasks that waited for it wait for the
  last new one. Dash asks first when deleting it also dismisses its scan issues.

## The board

The board's columns follow a task: *Drafts*, *Queued*, *Running*, *Review*, *Merging* and
*Merged*. On a phone it shows one column at a time; tap a tab or swipe sideways for the next.

- **New task** is at the top right of the board. On a phone it is the **+** button.
- The magnifier at the top searches the cards' titles and instructions as you type.
  <kbd>Esc</kbd> clears it.
- The filters show beside it once there is something to filter: **Mine**, **From scans (N)** and
  **Tag**, a list of the project's tags.
- The **–** button in a column's head minimizes the column to a thin strip. Click the strip to
  open it again.
- Click a card to open the task in a pane beside the board. The card stays marked; click another
  card to switch. <kbd>Esc</kbd> closes the pane. On a phone the task opens full screen, with
  **Back**.

## Tags

A task can have tags: short labels such as `backend` or `docs`. They show as chips on the card
and in the task, each in a colour of its own: the same tag has the same colour everywhere, and
severity tags (`critical`, `high`, …) have their severity's colour. Set them under **Tags** in
the task form, or with `#tag` in **New task**; edit them under **Tags** in the task. Type a tag
and press <kbd>Enter</kbd> or a comma to add it; <kbd>Backspace</kbd> in the empty box takes the
last one off. The project's other tags are offered as you type. A task has at most 20 tags, each
at most 40 characters. Click a tag on a card, or pick one under **Tag** above the board, to show
only tasks with that tag; click its chip above the board to show every task again.

## Drafts from reported problems

Problems that scans find, that Claude reports during a task (`report_issue`), or that are sent to
the API become draft tasks. Each is ready to queue: its prompt holds the title, details, file and
line, suggested fix, severity and type. It gets tags for its type (`bug`, `security`, `issue`),
its severity and its source (`scan:<scan name>` or `reported`).

- A problem a task already has adds no draft. That task's card shows how many times it was
  reported, with its severity, e.g. "low · 3 reports".
- Deleting such a draft dismisses the problem: it won't come back as a draft. See
  **Scans → Dismissed** to undo it.
- A problem that comes back after its task was merged gets a new draft that names that task.
- Drafts from scans below `high` severity show only under the **From scans (N)** chip, which
  shows only the tasks scans added. **⋮ → Scans…** opens the scans.
- To combine drafts into one, pick two or more and click **Combine**. See [Queue it](#queue-it)
  for how to pick drafts.

See [Scans](scans.md#findings-on-the-board) for how Dash spots the same problem twice.

## Queue it

Every way of making tasks ends with the same two buttons: **Save draft** (the highlighted one)
keeps it in the *Drafts* column to queue later, **Queue** queues it. In the **New task** sheet,
<kbd>Enter</kbd> saves a draft. In both the sheet and the full form,
<kbd>Ctrl</kbd>+<kbd>Enter</kbd> (<kbd>⌘</kbd>+<kbd>Enter</kbd> on a Mac) queues; in the full
form <kbd>Ctrl</kbd>+<kbd>S</kbd> saves a draft.

To queue a draft from the board, point at its card and click **Queue**. To queue several, tick
the box beside **Queue** on each, or click **Select** at the foot of *Drafts* (on a phone, that is
the way to pick). The bar at the bottom then has **Queue** for the picked drafts, **Select all**,
**Combine** (from two) and **Clear**. <kbd>Esc</kbd> clears it too.

The task moves to *Queued*, then *Running* as soon as Claude starts on it in its own worktree.
Open the card to follow along. If Claude needs permission for something, Dash asks you.

### When Claude asks a question

Claude can stop to ask you something. The task then shows a question card instead of
**Allow** and **Deny**: each question with its choices, and an **Other** field for an answer
in your own words. Pick an answer for each question and click **Submit**. Claude carries on
with your answers.

Click **Decline** to skip the question. You can tell Claude why. A question nobody answers is
declined like any other request when it times out.

A chat asks its questions the same way: the card shows in the chat, with **Submit** and
**Decline**.

Queue as many tasks as you like: they run in parallel, each in its own worktree.

### Tasks that name the same files

Before a queued task starts, Dash looks for a task in the same project that should go first.
That is a task whose title or prompt names a file this one's names too, and which is ahead of
it in the queue, running, in review or merging. Run side by side, the two would likely conflict
when they merge. So the task waits in *Queued* until that task is merged, and then starts on top
of its code. Its card says which task it waits for, and which file both name.

A file counts when it is written with its folder, such as `web/static/app/todos.js`. A name
with no folder counts only in backticks, such as `README.md`. URLs and absolute paths don't
count.

To run the task anyway, click **Start now** on its card. The two tasks may then conflict, and
Dash sends the conflict back to Claude to fix. Only a task's first run waits like this. A task
sent back with feedback, or with a conflict to fix, already has its branch, so it doesn't wait.

## Review

When Claude is done the task lands in *Review*. Its card shows Claude's summary of what it did
instead of your prompt. Click **Review** on the card (or open it) and
read the diff on the **Changes** tab. The spark button on the card (**Ask Claude**) has Claude
review it first. A changed image
(PNG, JPEG, GIF or WebP) shows as pictures, before and after, here and in **History**.

- Not right? Click **Rework…**, say what to change, and it goes back to Claude.
- For a small fix, click **Edit** on a changed file: saving commits your edit on the task's
  branch, so approving merges it with the rest.
- The task's prompt shows folded to its first line. Click **Show** to read all of it.
- **Details**, folded above the tabs, holds the test results and **Token use**: each Claude run
  on the task, with its tokens (and how many were cached), turns and cost. When the tests failed,
  **Details** has a **Tests fail** badge.

## Approve

Click **Approve**. The task goes through *Merging* and ends up in *Merged*, as one squashed
commit on the default branch, pushed if you like. See [Merge queue](merge-queue.md). The
*Merging* column shows only while a task is merging.

**Approve** on a card waits a few seconds before the merge starts: click **Undo** in the toast to
keep the task in *Review*. In a narrow column the card's **Approve** is a check-mark button.

## Move cards

Drag a card to another column to act on it (on a phone, long-press it and drop it on a button
at the bottom). While you drag, each column you can drop on says what the drop does:

- On *Queued*: **Queue** a draft, or **Send back** a task in *Review* for rework.
- On *Drafts*: **Cancel task**. A task with an open pull request has it closed. The toast has
  **Undo** for a few seconds before anything happens.
- On *Merged*: **Approve** (or **Close** a task with no changes), with **Undo** as on the card.
- In the same column: change the order.

When the columns don't fit the window, the board scrolls sideways and its edge is shaded.

In a GitHub project (with pull requests, the default) the review happens on GitHub instead: when
Claude finishes, Dash opens a pull request and the task waits in *Review* with it linked. Reviews
asking for changes go back to Claude. Once it is approved and its checks pass, you merge it, on
GitHub or with **Merge pull request** in Dash (Dash never merges one on its own). See
[GitHub](github.md).

## Tasks with no changes

A task Claude finishes without changing any files (an answer, an investigation) has nothing to
merge. Its card in *Review* says *No changes* and shows Claude's answer. Click **Close** to close
it, or **Ask for changes…** to send Claude a message and have it run again. A closed task is in
*Merged*, marked *Closed · no changes*, with no commit. To skip Review for these tasks, see
[Skip Review for tasks that change no files](boards.md#skip-review-for-tasks-that-change-no-files).

## Follow-up tasks

To continue where a finished task left off, pick it under **Build on** in the full form. The new
task's first run continues that task's Claude session (as a fork; the earlier session stays as it
was), so Claude doesn't explore the code again. The list shows the project's recently merged or
failed tasks.

A task that builds on an unfinished one waits in *Queued* until it is merged or failed.
A task with dependencies waits the same way, and its card says "waits for" and the task's name.
When that task is still a draft, click **Queue both** on the card. When it failed, the line is red. The
task's detail shows **Builds on #id title**. On a wide screen, change it there under **Build on**
while the task is a draft, queued or failed.

If the earlier session isn't on the computer that runs the task (it ran on another runner, or the
session was deleted), the task starts a fresh session with its full prompt, and its log says so.
On a hub, Dash sends the task to the runner that ran the earlier one when it can.

## Who made a task

The task's pane says who created it ("Created by Ana", or "Created by Dash" for a task Dash made
itself). When your organization has more than one member, each card also shows its creator's
picture or initial, and the filter row above the board has **Mine**: press it to show only the
tasks you created, and again to show everyone's (or type `:mine`). Your browser remembers the
choice. A task whose creator has left the organization, or deleted their account, shows no
creator.

## Limit a project's tasks at once

In **Project settings → Commands**, set **Tasks at once**. At most that many of the project's
tasks run at the same time, on this computer and on its runners together. The others stay queued,
and say why, until one finishes. A new project starts at 5. Clear it for no limit of the
project's own. The API field is `max_parallel` (`0` for no limit).

## Weekly pace

The queue pauses when a Claude usage window reaches its limit. To spread the week's usage evenly
instead, turn on **Follow weekly pace** in the usage sheet (click the usage meter). Ahead of the
pace line no new tasks start until the line catches up. Behind it more run at once: one more per
3 hours behind, up to a cap you set (twice the usual by default). Running tasks are never
stopped.

The setting belongs to a Claude account, since runners logged in to the same account share its
limits. The hub groups runners by the account email each reports (the Runners page shows it, with
the plan) and paces each group as one budget, using the freshest usage any of them reported.
Ahead of the line none of them gets new tasks. Behind it, the extra tasks are spread over the
group's runners, up to the boost you pick times their combined usual. The owner of a runner on
the account or an org admin can change it; the hub keeps it in `pace.json`. A runner that doesn't
report an account (an older Dash) is paced on its own, and a runner whose hub doesn't pace (an
older hub) paces itself from its own setting in `usage.json`.

When your runners are on more than one Claude account, the **Claude usage** sheet shows each
account's meters under its email, with the runners on it. Pick the account under **Settings for**
to see and change its pace. The queue shows as paused only when every account waits; while one
still has usage, new tasks go to its runners.

## Start now

While the queue waits for the pace, each queued card says "Waiting for weekly pace", with
**Start now** beside it. To run one task anyway, click **Start now** on its card (or in the task,
or **⋯ → Start now**). It starts as soon as a lane is free; pacing goes on for the other tasks.

**Start now** also runs a task that waits for another to go first (see
[Tasks that name the same files](#tasks-that-name-the-same-files)).

Only the pace and that wait are skipped: a usage limit, a paused queue, the project's **Tasks at once** and
unmerged dependencies still hold it back. The task's detail says it starts now, and once it runs,
that the run counts toward usage, so the queue may wait a little longer afterwards. Start now is
used up by that run: later runs (a rework, a retry) follow the pace again.

The API is `POST /api/todos/{id}/start-now`. A chat can do it with `queue_todo` and
`start_now: true` (see [MCP](mcp.md#the-chats-task-tools)).

## Pause a project

To hold all of a project's work, choose **⋯ → Pause this project** in the top bar. Its running
tasks stop and go back to *Queued*, paused, keeping their worktree and Claude session; on a hub,
their runners stop them. Its queued tasks are paused too, and no task of the project starts,
including new ones, until you choose **⋯ → Resume this project** (or **Resume project** in the
banner above the board).

Resuming continues each task in its session. A task you paused on its own before stays paused. A
task on a runner that can't be reached keeps running, and Dash says so. The API is
`POST /api/projects/{id}/pause` and `POST /api/projects/{id}/resume`.

## Pause the queue

Platform admins can stop every project's queue at once: choose **⋯ → Pause queue (all
projects)**. No new task starts in any project of this Dash; running tasks finish. It doesn't
stop running work, as **Pause this project** does. Choose **⋯ → Resume queue (all projects)** to
start again. The queue also pauses by itself when a Claude usage window reaches its limit (see
[Weekly pace](#weekly-pace)).
