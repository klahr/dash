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
