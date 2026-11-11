# Wall view

A read-only, full-screen dashboard of an organization's work for a TV in the office, at
`/wall`. It is laid out for 1920×1080 and scales to fill any landscape screen (16:10, 4:3 and
ultra-wide screens are filled, not letterboxed). Narrow or portrait screens (a laptop window, a
portrait TV, a phone) get a stacked layout that scrolls. Nothing on it needs a click.

From top to bottom: whether the queue runs and how many runner slots are busy, the Claude
usage of the 5-hour and 7-day windows (with the time left until each resets, e.g. "resets in 3d 4h"), a clock; the
number of tasks in each stage; a card per running task (elapsed time, run, cost so far,
lines changed, the tool call it is making); what needs a human (reviews oldest first, failed
tasks, open issues; red when waiting too long); the runners; today's merges, spend and
merges per hour; and a ticker of what happened lately.

It refreshes every 5 seconds.

## Pull requests under "Needs a human"

In pull request mode a task stays in Review for as long as its pull request is open on
GitHub (see [GitHub](github.md)). The wall lists it under "Needs a human" only while a
person has to act on GitHub, and says what next to `PR #n`:

| The pull request | Listed? |
| --- | --- |
| A draft (its checks still run, or Claude fixes failing ones) | No |
| Changes requested (the review went back to Claude) | No |
| Ready for review, not approved yet | **Awaiting review** |
| Approved (or the project approves automatically), waiting for its checks | No |
| Approved (or the project approves automatically) and green, waiting for a person to merge it (Dash never merges a pull request) | **Waiting to be merged on GitHub** |
| A failing check no code change fixes (e.g. an unsigned CLA) | **Blocked on GitHub** |

What a pull request waits for is what Dash last saw while following it; the wall makes no
GitHub calls of its own. Its timer (red after an hour) runs from when the pull request
started waiting for a person, or from when Claude last finished if that is later or wasn't
recorded. Review tasks without a pull request (merge-it mode, or a pull request mode task
whose tests failed before one was opened) are always listed, timed from when Claude
finished.

The Review count in the stages bar is every task in Review; "N on GitHub" says how many of
them wait on GitHub, Claude or their checks rather than a person.

## Opening it

Signed in, open `https://<your dash>/wall`: it shows your active organization, across all
of its projects.

**Wall view** in the Account dialog (account menu → Account) configures it: pick a project and privacy mode, open the
wall with them, and (org admins) make and revoke TV links, below. The options are kept in
that browser; they become part of the link.

Options (combine them with `&`):

| Option | Effect |
| --- | --- |
| `?project=<name or id>` | Only that project |
| `?privacy=1` | The public view (below) |
| `?token=<wall token>` | Open it without signing in (below) |

## A link for a TV

A TV shouldn't keep someone's session. An org admin makes a **wall token** instead: it
opens `/wall` (and its data, `/api/wall`) for that organization and nothing else, not the
API and not the dashboard. Make one in **Account → Wall view → Links for TVs**, or with the API:

```sh
# make one (as an org admin, signed in; the secret is shown only now)
curl -b cookies.txt -X POST https://dash.example.com/api/wall/tokens -d '{"name":"Office TV"}'
# → {"token":{"id":3,…},"secret":"dashw_…","path":"/wall?token=dashw_…"}

curl -b cookies.txt https://dash.example.com/api/wall/tokens            # list them
curl -b cookies.txt -X DELETE https://dash.example.com/api/wall/tokens/3 # revoke one
```

Open `https://dash.example.com/wall?token=dashw_…` on the TV. Anyone with the link sees
the wall, so treat it like a password; add `&privacy=1` where visitors walk past.

## The public view

The public view shows only what anyone may see: the counts in each stage, timers, lines
changed, runner slots, Claude usage and merges per hour. It hides task titles and
prompts, project names (they become "Project A", "Project B"…, lettered by when the
project was made), runner names ("Runner A"…), why a task failed, why it was sent back
(only "merge conflict" or "failing tests"), activity lines, issue titles and costs. With
it, `?project=` takes a project's id only, so names can't be guessed.

Open it with `?privacy=1`, or make a **public wall token**: its wall is always the public
view, whatever its link says, so it is the one to put on a screen outside the office or
share outside the organization.

```sh
curl -b cookies.txt -X POST https://dash.example.com/api/wall/tokens -d '{"name":"Lobby","public":true}'
```

## What it doesn't show yet

"Spend" is the cost of the tasks worked on today (Dash keeps cost per task, not per run).
"First-try merges" are today's merges that took a single Claude run. Time from queue to
merge isn't recorded, so it isn't shown. The activity line comes from the events this Dash
saw since it started.
