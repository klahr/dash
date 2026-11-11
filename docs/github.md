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
   opens one anyway.
2. **The checks run.** Failing checks and conflicts with the base branch go back to Claude, within
   the project's automatic fixes, and its fixes are pushed to the same pull request. Checks no
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
   aren't sent, and neither are comments in the pull request's conversation tab: to ask Claude
   for something, leave it in a review (a single comment on a line is a review too). You can
   still **Rework** from Dash as well; that also ends up on the same pull request.
5. **Merging.** With **Merge pull requests when checks pass** (on by default), Dash squash-merges
   the pull request once GitHub finds it mergeable, its checks pass and it has an **approving
   review on GitHub** (from a person: an app's approval doesn't count; when the branch requires
   reviews, GitHub's own verdict decides). A request for changes holds it until that reviewer
   approves. Turn it off to merge on GitHub yourself. Either way, the task is marked merged
   when the pull request is, and its branch is deleted. If GitHub refuses Dash's merge (a second
   required review, say), the pull request stays open for you to merge there.
6. **Rejecting.** Closing the pull request on GitHub without merging rejects the task: it moves
   to *Failed*, saying so; **Rework** it to have Claude try again in a new pull request, or
   delete it. The other way round, rejecting the task in Dash, by deleting it or moving it back
   to drafts, closes its pull request on GitHub (with a comment saying it was rejected in Dash)
   and deletes its branch there.

GitHub doesn't let you approve your own pull requests, and Dash opens them with the `gh` account
of the computer running the task. If that is your account and nobody else reviews, either merge
on GitHub yourself, or turn on **Approve tasks automatically** (below).

Tasks that were already in Review without a pull request when Dash started opening them on
finishing keep working: **Approve** opens their pull request, which is then reviewed on GitHub
like the others. Pull requests opened by an approval in Dash before then are followed the same
way: they now merge once approved on GitHub too.

## Approve tasks automatically

With **Approve tasks automatically**:

- **Merge it** mode: finished tasks whose tests pass go straight to the merge queue and the
  Review column is hidden; tasks with failing tests still wait there for you.
- **Pull request** mode: pull requests don't wait for an approving review. Dash marks a task's
  pull request ready for review once its checks pass, and (with **Merge pull requests when checks
  pass** on) merges it then, as it did before reviews moved to GitHub. A review asking for
  changes submitted before that still goes back to Claude and holds the merge until the reviewer
  approves. The Review column shows the tasks whose pull requests are open.

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

When the project is on GitHub, Dash then follows the workflow runs the tag started: the builds
pill shows "Building v1.2.3", and the Builds sheet shows it building, published (with a link to
the GitHub release) or failed, with **Ask Claude to fix it** for a failure.

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
