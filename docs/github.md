# GitHub

A GitHub project is made on the hub with **Add project** → **From GitHub** or **New GitHub
repository** (with the picked runner's `gh`; the hub runs no `gh` of its own), or **From URL**. A
**Local only** project has no remote: to put one on GitHub, create the repository from its folder
on the runner (`gh repo create --source . --push`), then make a project of it on the hub. In the
project's settings, under **How finished tasks land**, choose:

- **Open a pull request** (the default for a GitHub repository, when the runner has `gh`): the
  review happens on GitHub. See [Pull requests](#pull-requests) below.
- **Merge it**: you review the task in Dash's Review column, and **Approve** makes it one squashed
  commit on the default branch, with no GitHub checks first; the only choice for a project
  without a GitHub remote, or when its runner has no `gh`. The
  [runner](several-computers.md) does this in its clone and always pushes to origin, pulling
  first; if someone pushed first it merges again on top of their work.

Until you pick one yourself, the project follows the default: connecting a GitHub repository
switches it to pull requests (and a runner without `gh` merges instead). Once you choose, your choice
stays.

## Pull requests

In pull request mode the pull request is where a task is reviewed:

1. **Claude finishes the task**: Dash (or the task's runner) pushes its branch and opens a pull
   request against the branch tasks land on, as a **draft**. The task moves to **Review** with
   the pull request linked. (Where GitHub has no draft pull requests, e.g. a private repository
   on a free plan, it opens ready for review.) A task whose tests fail locally, once Claude's
   automatic fixes are used up, waits in Review without a pull request, as before: **Approve**
   opens one anyway. The pull request's title and description, and every comment Dash posts,
   tag no one: an `@handle` in the task's prompt or Claude's summary is put in inline code
   (`` `@someone` ``), so GitHub notifies no one.
2. **The checks run.** Failing checks and conflicts with the base branch go back to Claude, within
   the project's automatic fixes (conflicts with other tasks show on the card before that; see
   [Merge queue](merge-queue.md#conflicts-found-early)), and its fixes are pushed to the same pull request. Checks no
   change to the code can fix (an unsigned CLA, a check that runs from the base branch) are
   shown on the card, and Dash carries on once they pass. When the checks pass, Dash marks the
   draft **ready for review**.
3. **You review it on GitHub.** The Review column now means "waiting on GitHub": the card shows
   the pull request's link, its checks (hover for each one) and where its review stands:
   *Draft*, *Awaiting review*, *Approved* or *Changes requested*. **Approve** isn't offered for
   it; the detail pane links to the pull request instead.
4. **A review asking for changes goes back to Claude.** When someone submits a review on GitHub
   that requests changes or comments (in the review's summary, or on lines of the diff), Dash
   sends it to Claude the way a **Rework** from Dash is sent: the review's text and each comment
   with its file and line. Claude's fix is pushed to the same pull request, and the task returns
   to Review. Each review is sent once. Reviews by apps (CI, coverage or CLA bots) and approvals
   aren't sent. You can still **Rework** from Dash as well; that also ends up on the same pull
   request.

   **Comments in the conversation tab** go back to Claude the same way, when they have the
   command `/dash` (e.g. "`/dash` also update the docs"); `/claude` works too. It counts as a
   whole word, in any case, but not in a link or a path, nor inside code. Don't tag Dash or Claude
   with an `@`: those are other people's GitHub accounts, and Dash ignores it. The rest are taken
   as talk between people ("merging after the release") and left alone: every comment sent starts
   a new run of Claude and a new commit on the pull request, so asking for one is explicit.
   Comments by apps (including the Claude GitHub app's), empty ones and Dash's own (its answers
   below) aren't sent. Each comment is sent once, also after Dash restarts; comments and reviews
   that arrive between two polls go to Claude together in one note, with the same fresh budget of
   automatic fixes as a review. Comments already on a pull request opened before Dash followed
   the conversation tab aren't sent; newer ones are.

   Only reviews and comments by people who can push to the repository go to Claude: its owner,
   members of its organization and collaborators. On a public repository anyone can comment,
   and Claude runs with the shell allowed, so everyone else's are skipped. The task's log says
   whose were skipped. An approval counts only from these people too.

   Dash then answers on GitHub, so you don't have to work out from the commits what happened:

   - **A comment with Claude's summary** of what it changed, listing each comment it disagreed
     with and why it left the code as it is.
   - **A reply in each line comment's thread**: *Addressed*, with what changed, or *Not
     changed*, with why. Threads Claude addressed are resolved; those it pushed back on stay
     open for you to answer.
   - **A new review request** to whoever asked for changes.

   If the fix fails instead (Claude stops, or the merge fails) and nothing new is pushed, Dash
   comments on the pull request saying why; rework the task in Dash, or leave another review, to
   have Claude try again. Each review and comment is answered once, also when Dash restarts in between. A
   failure to post on GitHub doesn't fail the task: it is noted in the task's log.

   To tell its answers apart, Dash numbers the review's text, each line comment and each conversation comment in what it
   sends Claude, and asks it to end its final message with a block answering each one:

   ````
   ```dash-review
   [1] addressed: renamed the file to notes.md
   [2] disagreed: the constant is used once; inlining it reads better
   ```
   ````

   The rest of the message is the summary. Without the block, Dash posts the summary and asks
   for the review again, but doesn't reply in or resolve any thread.
5. **Merging.** Dash never merges a pull request on its own: a person merges it, on GitHub or
   from Dash. Once its checks pass and it has an **approving review on GitHub** (from a person:
   an app's approval doesn't count; when the branch requires reviews, GitHub's own verdict
   decides), the card reads *Approved; waiting for you to merge on GitHub* and the wall lists it
   under "Needs a human". A request for changes holds it until that reviewer approves.

   To merge it from Dash, click **Merge pull request** on the task or on its card and
   confirm. It shows once the pull request's checks pass, also before anyone approved it: GitHub
   decides whether it may merge. Anyone who can approve tasks in the project can merge.

   - **Who merges.** The merge is done with the `gh` account signed in on the computer that
     follows the pull request: the computer running Dash, or, on a hub, the runner that ran the
     task. GitHub shows that account as the one who merged it.
   - **How.** **Project settings → Approval & push → Merge pull requests as** picks squash, a merge
     commit or rebase. The default is the repository's: the method you last used there, or the
     first one it allows.
   - **Branch protection.** Dash never bypasses it (no `--admin`). If GitHub can't merge the pull
     request (a conflict, a failing required check, a missing review), Dash shows GitHub's message
     and the task stays as it is.
   - **Merge queue.** If the branch merges through a merge queue, the pull request joins it and
     Dash says *Queued to merge*. The task is marked merged once the queue merges it.

   The task is marked merged when the pull request is, and its branch is deleted. After a merge
   from Dash that happens at once, without waiting for the next check of GitHub.
6. **Rejecting.** Closing the pull request on GitHub without merging rejects the task: it moves
   to *Failed*, saying so; **Rework…** it to have Claude try again in a new pull request, or
   delete it. The other way round, rejecting the task in Dash, by deleting it or moving it back
   to drafts, closes its pull request on GitHub (with a comment saying it was rejected in Dash)
   and deletes its branch there.

GitHub doesn't let you approve your own pull requests, and Dash opens them with the `gh` account
of the computer running the task. If that is your account and nobody else reviews, merge it
yourself once its checks pass: click **Merge pull request** in Dash, or merge it on GitHub. Turn
on **Approve tasks automatically**, below, to have Dash treat it as approved: it then waits for
your merge rather than for a review. If the branch requires an approving review, GitHub still
refuses the merge until someone else approves.

Tasks that were already in Review without a pull request when Dash started opening them on
finishing keep working: **Approve** opens their pull request, which is then reviewed on GitHub
like the others. Pull requests opened by an approval in Dash before then are followed the same
way: they move back to Review and wait for a person to merge them too.

## Approve tasks automatically

With **Approve tasks automatically**:

- **Merge it** mode: finished tasks whose tests pass go straight to the merge queue and the
  Review column is hidden; tasks with failing tests still wait there for you.
- **Pull request** mode: pull requests don't wait for an approving review. Dash marks a task's
  pull request ready for review once its checks pass, and it then waits for a person to merge it,
  on GitHub or with **Merge pull request** in Dash: Dash never merges it on its own. A review asking for changes still goes back to Claude and
  holds it until the reviewer approves. The Review column shows the tasks whose pull requests are
  open.

Merged tasks can be reverted with one click (`u`).

## Work on another branch

Tasks start from the project's default branch (`main` or `master`) and land on it, unless you pick
another one: click the branch button in the top bar (or **Switch branch…** in the project menu).
The switcher lists the checkout's branches and origin's (it fetches them when it opens); type to
filter, or type a new name to create that branch from the current one. Once switched, new tasks
start from that branch and are merged into it (or, in pull request mode, open their pull requests
against it), and History, Files and the push button follow it. Tasks that had already started keep
the branch they started from. A branch origin doesn't have yet is pushed when you switch to it, so
pull requests and pushes have somewhere to go.

Off the default branch, the switcher shows how far the branch is ahead of the default one and offers
**Create pull request into main**: Dash pushes the branch and opens (or finds) its pull request,
listing the tasks that landed on it. Merge it on GitHub, then switch back to the default branch.

The same is available as `GET /api/projects/{id}/branches` (`?fetch=1` fetches origin first),
`PUT /api/projects/{id}/branch` (`{"branch": "feature", "create": true}`) and
`POST /api/projects/{id}/branch/pr` (optional `title` and `body`). For a project whose tasks run on
runners, each runner's clone follows the hub's branch.

## Builds

When the project is on GitHub, Dash follows the GitHub Actions runs on the default branch:

- **Running**: a small pulsing ring shows beside the project's name in the sidebar. Hover it to
  see the branch ("Build running on main"), and click it to open the builds.
- **Passing**: nothing shows.
- **Failing**: a red **Build failing** chip shows in the sidebar (on a phone, the menu button gets
  a red dot). Click it to open the builds.

**Builds** in the project menu (top bar) opens the same sheet at any time: the latest runs,
**Re-run build**, and after a failure **Retry failed jobs** and **Ask Claude to fix it**.

When a build fails, Dash queues a task asking Claude to fix it ("Fix failing build: …"), unless
**Project settings → Approval & push → Fix failed builds automatically** is off. The task shows as
made by Dash. On a hub it runs on the runners of a member: who turned that setting on, else who
added the project, else the org's owner ([whose work a runner does](several-computers.md#whose-work-a-runner-does)).

## Tag a release

In **History** (the commit graph of the branch tasks land on), click a commit (or move to it with
`j`/`k`) to open it in a pane beside the graph (full screen on a phone): its whole message, author
and committer, parents, and the files it changed, each with its diff. The pane's head has
**Tag release…**, **Copy SHA** and **Open on GitHub**. Commits origin doesn't have yet (the dimmed
"Not pushed yet" rows) can't be tagged: push first. `Esc` closes the pane.

**Tag release…** fetches origin's tags, then offers the next patch, minor and major version after
the latest release tag (the highest version, not the newest tag), or 0.1.0 for the first one, plus
any other version you type. A tag that already exists, here or on origin, is refused. The release
notes start out as the Dash tasks merged since the previous release (each task's title and Claude's
summary of it); edit them freely, they become the tag's message. The dialog warns when the commit's
build isn't green or tasks are still running. Confirming creates an annotated tag
(`git tag -a <tag> <sha>`) and pushes it (`git push origin <tag>`); the tag shows up in the graph.

When the project is on GitHub, Dash then follows the workflow runs the tag started: the
project's row in the sidebar shows the pulsing ring ("Building v1.2.3"), a failed release shows a
**Release v1.2.3 failed** chip, and the **Builds** sheet shows it building, published (with a link
to the GitHub release) or failed, with **Ask Claude to fix it** for a failure.

Only the organization's owners and admins, who may push, can tag releases. For a project on
another computer, the tag is made and pushed there, in its own clone.

### Tag format

Each project names its tags with a format, in the project settings: `X`, `Y` and `Z` stand for the
major, minor and patch version (each exactly once, in that order) and anything else is kept as it
is, e.g. `X.Y.Z` (the default), `vX.Y.Z`, `release-X.Y.Z` or `app/X.Y.Z`. It must make a valid git
tag name. Tags in another format are ignored when working out the latest version. Pre-release
suffixes such as `-rc.1` aren't supported yet.

If the repository has a GitHub Actions workflow that runs on pushed tags (`on.push.tags` in
`.github/workflows/*.yml`) and the format's tags wouldn't match it, the settings say so and offer a
matching format: with `X.Y.Z` and a workflow on `v*`, pushing a tag wouldn't start the release.
(Dash's own repository releases on `[0-9]+.[0-9]+.[0-9]+*` tags, so its project uses the default
`X.Y.Z`.)

Besides the History view, a release can be tagged with `POST /api/projects/{id}/tags`
(`{"bump": "minor"}` or `{"version": "1.4.0"}`, optional `sha` and `notes`) or the
`create_release` tool ([MCP](mcp.md)).

Dash fetches origin's tags along with its default branch, and when the History view is shown, so
tags pushed from elsewhere appear too.
