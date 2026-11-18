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
   exactly that commit. Failing tests go back to Claude the same way, so a red build never lands.
4. **Land.** Dash squashes the branch onto the default branch (one commit per task) and pushes
   it, or, in pull request mode, pushes the branch and opens a pull request (see
   [GitHub](github.md)).

Claude gets a limited number of automatic fixes per task (**Project settings → General →
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

- **On the card:** "Conflicts with *Fix login* in auth.go +2". Hover it for every conflict and
  file.
- **In the task's detail:** each task (click it to open it) or the default branch it conflicts
  with, and all the files.

The check goes away once the conflict does: a branch changes, or the other task is merged,
fails or is sent back.

When a task gains a conflict with a task ahead of it in the merge queue (one that is merging),
Dash adds a note for Claude naming that task and the files. Claude reads it on its next run of
the task: a test fix, a rework, or when the merge queue sends it back. The task's log says so.
Each conflict is noted once.

A runner reports its tasks' conflicts to the hub, which shows them on the hub's board.

The check needs git 2.38 or newer. With an older git, Dash logs that the check is off and
conflicts are found at merge time, as before.
