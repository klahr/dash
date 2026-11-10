# GitHub

A GitHub project is made on the hub with **New project** → **Your GitHub repos** or **New GitHub
repo** (with the picked runner's `gh`; the hub runs no `gh` of its own), or by pasting its URL. A
**Local only** project has no remote: to put one on GitHub, create the repository from its folder
on the runner (`gh repo create --source . --push`), then make a project of it on the hub. In the
project's settings, choose what approving a task does:

- **Open a pull request** (the default for a GitHub repository, when the runner has `gh`): Dash
  pushes the branch and opens a PR, so GitHub's checks run before the work reaches the default
  branch. Failing checks and conflicts go back to Claude. With **Merge pull requests when checks
  pass** (on by default), Dash squash-merges the PR itself once GitHub finds it mergeable and its
  checks pass: your approval was the review. Turn it off to merge on GitHub yourself; the task is
  marked merged when the PR is. If GitHub refuses the merge (a required review, say), the PR stays
  open for you to merge there.
- **Merge it**: one squashed commit on the default branch, with no GitHub checks first; the only
  choice for a project without a GitHub remote, or when its runner has no `gh`. The
  [runner](several-computers.md) does this in its clone and always pushes to origin, pulling
  first; if someone pushed first it merges again on top of their work.

Until you pick one yourself, the project follows the default: connecting a GitHub repository
switches it to pull requests (and a runner without `gh` merges instead). Once you choose, your choice
stays.

With "Approve tasks automatically", finished tasks whose tests pass go straight to the merge queue
and the Review column is hidden; tasks with failing tests still wait there for you.

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
