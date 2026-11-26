# Status

Dash keeps everything about its state in one place: the **status icon** in the top bar.

- With all well, the icon is quiet.
- When something needs you, it shows a dot: amber when it can wait, red when it can't (live
  updates are offline, Claude has a major outage, the queue is stopped, a runner's disk is full,
  the board is read-only).

Click it, press `Ctrl-k` and choose **Status**, or type `:status` (see [Keyboard](keyboard.md)). The
popover opens under the icon; on a phone it opens as a sheet across the bottom of the screen.
`Esc` closes it.

Each row says what is going on, and its button goes to where you fix it:

| Row                   | Shows                                                            | Goes to                                   |
| --------------------- | ---------------------------------------------------------------- | ----------------------------------------- |
| **Live updates**      | whether the page gets live updates                               | **Reload**                                |
| **Billing**           | the organization needs a subscription ([billing](billing.md))    | **Organization → Billing** (owners)       |
| **Claude**            | services affected on [Claude's status page](claude-status.md)    | the incidents                             |
| **Claude usage**      | the usage windows (per Claude account, with several), and why the queue waits | **Claude usage**, with the pace settings  |
| **Runner**            | the project has no runner it can use                             | **Add a runner**, its labels or **Runners** |
| **Disk space**        | runners nearly out of disk space                                 | the runner in **Runners**                 |
| **GitHub sync**       | the default branch can't sync with GitHub                        | the **Shell**                             |
| **Origin**            | the default branch is ahead of or behind origin's                | **Fetch now**                             |
| **GitHub rate limit** | GitHub's rate limit paused Dash's polls ([GitHub](github.md#rate-limits)) | **Builds**                       |
| **Checkouts**         | runners' checkouts left behind origin, each with **Pull**        | **Pull**                                  |
| **Merge queue**       | tasks waiting to land ([merge queue](merge-queue.md))            | the **Board**                             |
| **Previews (n)**      | running [previews](previews.md), on every computer               | the previews, with **Stop all**           |
| **Runners**           | the organization's runners, online or not, with their tasks      | a runner, or **Runners**                  |

A row shows only while it has something to say, except **Live updates**, and **Claude usage**
and **Runners** where Dash has them.

## Banners on the board

Only what stops work is a banner above the board:

- a runner's disk is too full to start a task;
- the project is paused (**Resume project**);
- Claude asks for a permission or a question.
