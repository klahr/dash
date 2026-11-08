# GitHub

Connect a repository from Projects → Create GitHub repository (uses `gh`). In the project's
settings, choose what approving a task does:

- **Merge it here:** one squashed commit on the default branch. With "Push after every merge",
  Dash pulls from GitHub first, so the push isn't rejected.
- **Open a pull request:** Dash pushes the branch and opens a PR. Failing checks and conflicts
  go back to Claude, and the task is marked merged when the PR is merged on GitHub.

With "Approve tasks automatically", finished tasks whose tests pass go straight to the merge queue
and the Review column is hidden; tasks with failing tests still wait there for you.

Merged tasks can be reverted with one click (`u`).

## Tag a release

In **History** (the default branch's commit graph), click a commit for its menu: **Tag release…**,
**Copy SHA** and **Open on GitHub**. Commits origin doesn't have yet (the dimmed "Not pushed yet"
rows) can't be tagged: push first.

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
