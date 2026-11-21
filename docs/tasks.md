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

## Add a task

Click **New task** (or press <kbd>n</kbd>) and write what Claude should do, as you would in a
chat; Claude names the task. <kbd>Shift</kbd>+<kbd>Enter</kbd> adds a field for another task (a
pasted multi-line text stays one task).

**More options…** (or <kbd>Shift</kbd>+<kbd>N</kbd>) opens the full form, with a title, priority,
model, dependencies, the task it builds on and attachments.

- **Priority** is **None**, **High** or **Urgent**: urgent tasks start first, then high ones, then
  the rest, oldest first. The API takes the same levels; see [Task priority](mcp.md#task-priority).
- **Split…** has Claude propose smaller tasks for a large one, each reviewed and merged on its
  own. Edit, remove or add to them, then save them all, each one waiting for the tasks it builds
  on.

## Queue it

Every way of making tasks ends with the same two buttons: **Save draft** keeps it in the
*Drafts* column to queue later, **Queue** queues it. In the **New task** sheet, <kbd>Enter</kbd>
saves a draft and <kbd>Ctrl</kbd>+<kbd>Enter</kbd> (<kbd>⌘</kbd>+<kbd>Enter</kbd> on a Mac)
queues.

The task moves to *Queued*, then *Running* as soon as Claude starts on it in its own worktree.
Open the card to follow along. If Claude needs permission for something, Dash asks you.

### When Claude asks a question

Claude can stop to ask you something. The task then shows a question card instead of
**Allow** and **Deny**: each question with its choices, and an **Other** field for an answer
in your own words. Pick an answer for each question and click **Submit**. Claude carries on
with your answers.

Click **Decline** to skip the question. You can tell Claude why. A question nobody answers is
declined like any other request when it times out.

Queue as many tasks as you like: they run in parallel, each in its own worktree.

## Review

When Claude is done the task lands in *Review*. Click **Review** on the card (or open it) and
read the diff on the **Changes** tab. **Ask Claude** has Claude review it first. A changed image
(PNG, JPEG, GIF or WebP) shows as pictures, before and after, here and in **History**.

- Not right? Click **Rework…**, say what to change, and it goes back to Claude.
- For a small fix, click **Edit** on a changed file: saving commits your edit on the task's
  branch, so approving merges it with the rest.
- **Token use** lists each Claude run on the task: its tokens (and how many were cached), turns
  and cost.

## Approve

Click **Approve**. The task goes through *Merging* and ends up in *Merged*, as one squashed
commit on the default branch, pushed if you like. See [Merge queue](merge-queue.md).

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

A task that builds on an unfinished one waits in *Queued* until it is merged or failed. The
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

## Start now

While the queue waits for the pace, each queued card says "Waiting for weekly pace", with
**Start now** beside it. To run one task anyway, click **Start now** on its card (or in the task,
or **⋯ → Start now**). It starts as soon as a lane is free; pacing goes on for the other tasks.

Only the pace is skipped: a usage limit, a paused queue, the project's **Tasks at once** and
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
