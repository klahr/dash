# Merge queue

Approved tasks land on the default branch one at a time, through the project's merge queue.
Each project has its own queue, so a long test run in one project never holds up another.

## What happens to an approved task

1. **Sync.** A project that pushes to GitHub first brings its default branch up to date with
   origin.
2. **Merge the default branch in.** Dash merges the default branch into the task's worktree.
   A conflict sends the task back to Claude to resolve. Once fixed, it returns to the queue
   without another review.
3. **Test.** The project's **Test command** runs on the result, unless it already passed on
   exactly that code (the same commit, or the same files). Failing tests go back to Claude the same way, so a red build never lands.
4. **Land.** Dash squashes the branch onto the default branch (one commit per task) and pushes
   it, or, in pull request mode, pushes the branch and opens a pull request (see
   [GitHub](github.md)).

Claude gets a limited number of automatic fixes per task (**Project settings → Commands →
Automatic fix attempts**). When they are used up, the task fails and waits for you to send it
back for rework.

## Conflicts found early

Dash doesn't wait for the merge to find a conflict. For each project, it checks the branches of
the tasks that are running, in review or merging:

- each pair of them, and
- each of them with the default branch.

It merges them with `git merge-tree`, which needs no checkout and changes nothing. Only branches
that changed a file in common are merged, so the check stays cheap. It runs a few seconds after
a task commits or changes state, and about once a minute for changes Dash didn't make (a push to
the default branch, a fetch).

What it finds shows:

- **On the card:** a link marker, "↔ *Fix login*", or "↔ *Fix login* +2" when more tasks are
  linked. Hover it for every conflict and file; click it to open the other task. A conflict with
  the default branch alone shows as "Conflicts with main in auth.go".
- **In the task's detail:** each task (click it to open it) or the default branch it conflicts
  with, and all the files.

The check goes away once the conflict does: a branch changes, or the other task is merged,
fails or is sent back.

When a task gains a conflict with a task ahead of it in the merge queue (one that is merging),
Dash adds a note for Claude naming that task and the files. Claude reads it on its next run of
the task: a test fix, a rework, or when the merge queue sends it back. Each conflict is noted
once.

## Where tasks meet

Dash records each time two tasks meet, on both of them:

- a conflict found between their branches,
- a conflict note one gave the other (the task ahead in the merge queue gives it),
- an `active_work` call of one that listed the other (see [A task's tools](mcp.md#a-tasks-tools)).

These show:

- **On the card**, while the task is queued, running, in review or merging: the "↔" link
  marker above. It is orange while the task has a conflict. Hover it for what happened, for
  example "#12 Fix login also changes auth.go" or "From #12 Fix login, ahead in the merge
  queue: conflicts in auth.go; noted for Claude's next run".
- **In the task's detail:** every meeting, newest first, each with a link to the other task.
- **On the task's Timeline tab**, as marked entries that link to the other task:
  - "📨 From #12, ahead in the merge queue: conflicts in auth.go" when the note is given,
  - "📬 Note from #12 sent to Claude" when the next run gives it to Claude,
  - "👀 Checked other work: #12, #15" when the task calls `active_work`.

A task keeps its 30 latest meetings. A meeting that happens again (another `active_work` call
listing the same task) replaces the earlier one.

A runner reports its tasks' conflicts and meetings to the hub, which shows them on the hub's
board with the hub's task ids. The hub answers `active_work` for its tasks on runners, so it
records those calls itself.

The check needs git 2.38 or newer. With an older git, Dash logs that the check is off and
conflicts are found at merge time, as before.
