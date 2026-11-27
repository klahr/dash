# Wall view

A read-only, full-screen dashboard of an organization's work for a TV in the office, at
`/wall`. It is laid out for 1920×1080 and scales to fill any landscape screen (16:10, 4:3 and
ultra-wide screens are filled, not letterboxed). Narrow or portrait screens (a laptop window, a
portrait TV, a phone) get a stacked layout that scrolls. Nothing on it needs a click.

From top to bottom:

- **The header**: the overall status, the Claude usage of the 5-hour and 7-day windows (with the
  time left until each resets, e.g. "resets in 3d 4h"), and a clock.
- **The stage bar**: one short row with the flow of tasks, Queued → Running → Review → Merging.
  A pill beside it says when the queue is paused, or runs extra slots to catch up.
- **Running now**: a card per running task (below).
- **Needs a human**: reviews (oldest first), failed tasks and open issues (the tasks made from
  reported problems that aren't merged yet), each with who should act and how long it has waited.
- **Runners**, and **Today**: merges, spend, first-try merges and lines landed, and **Just
  landed**, the last five merges with their time.

Each number is in one place: merges and lines landed are in Today, failed tasks in Needs a human.

## The overall status

The header says in one line how things stand, e.g. "● All good · 5 running" or "▲ Waiting on
people · 5 running · 2 waiting on people", with the reasons below it. The screen gets an edge
in the same colour. Each state has a word and a symbol as well as a colour:

| Status | When |
| --- | --- |
| ● **All good** (green) | Nothing waits on people |
| ▲ **Waiting on people** or **Heads up** (amber) | A review or failed task waits, there are open issues, or a usage window is at 90% or more, or will reach 100% before it resets at the rate it is used |
| ✕ **Needs action** (red) | A review has waited over an hour or a failure over 30 minutes, the queue is paused, or a runner is offline (one last seen in the last 24 hours, or one with tasks only it can take) |

When a row in Needs a human waits too long, the row, the panel's border and its heading turn red,
and the heading says "⚠ waiting too long".

## Running tasks

A card shows the task, its project, the run (only from the second run), lines changed, the
elapsed time and the tool call it is making. When that tool call hasn't changed for 3 minutes,
the line says so: "stuck on go test ./... for 6m".

A bar compares the elapsed time with the typical time of the project's earlier runs of the same
kind (first runs, or later runs such as rework): the median of the last 10, as the build and
deploy estimates take it. It reads e.g. "8m / ~12m", turns amber with "over usual" past the
typical time and red with "2× usual" at twice it. A project with no finished runs yet shows the
elapsed time only.

When more tasks run than cards fit (about 5 at 1080p), the list switches to one line per task:
title, bar and time. It switches back once one fewer than that runs, so it doesn't flip on each
poll.

## Usage

When the runners are on more than one Claude account, the wall shows a set of meters per account,
from the newest report of the account's online runners. Signed in, it shows your own runners'
accounts (the org's if you have none), named by email. With a wall link it shows the org's runners' accounts, named by their
runners (in public mode by their aliases, "Runner A"), never by email. An account at its limit is
named in red. The queue shows as paused on limits only when every account that can run tasks is
out of usage.

When a window would reach 100% before it resets at the rate it has been used since the window
began, its meter says so in red ("100% in ~2h at this rate") and the status turns amber: the
queue pauses when it gets there.

## Today

Merges, spend and first-try merges each compare with an average day of the last 7, up to the
same time of day: "▲ above avg 9", "▼ below avg 9" or "= avg 9".

## Updates, stale data and motion

It refreshes every 5 seconds. Changes are animated: a new task's card slides in, a finished
one shows how it ended ("Ready for review", "Failed", "Done") before it leaves, and counts
roll to their new values. Nothing moves while nothing changes. A screen set to reduce motion
gets no animations.

When a refresh fails, or the last good one is older than about two refreshes, the board greys
out, the running timers stop, and a notice in the middle says "Last updated 4 min ago ·
retrying". The next good refresh brings it back.

## Off hours and burn-in

Outside work hours, with nothing running and nothing waiting on people, the wall dims to a
summary of the week ("This week: 84 merged · 91% first try") and the overall status. It comes
back as soon as a task runs or needs a human. Work hours are 08:00 to 18:00, Monday to Friday,
unless the link says otherwise (below).

So an OLED TV doesn't burn in, the whole board moves by a few pixels every 3 minutes.

## Colour and shape

Status is never shown by colour alone: runners are ● online, ◐ paused or ✕ offline, a finished
card says ✓ or ✕ and what happened, tests say ✓ or ✗, rows that wait too long have ⚠, and the
overall status has its symbol and word.

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
| Approved (or the project approves automatically) and green, waiting for a person to merge it (on GitHub, or with **Merge pull request** in Dash) | **Waiting to be merged** |
| A failing check no code change fixes (e.g. an unsigned CLA) | **Blocked on GitHub** |

What a pull request waits for is what Dash last saw while following it; the wall makes no
GitHub calls of its own. Its timer (red after an hour) runs from when the pull request
started waiting for a person, or from when Claude last finished if that is later or wasn't
recorded. Review tasks without a pull request (merge-it mode, or a pull request mode task
whose tests failed before one was opened) are always listed, timed from when Claude
finished.

The Review count in the stage bar is every task in Review; "N on GitHub" says how many of
them wait on GitHub, Claude or their checks rather than a person.

## Opening it

Signed in, open `https://<your dash>/wall`: it shows your active organization, across all
of its projects.

**Organization → Wall view** (account menu → Organization) configures it: pick a project, privacy mode and **Hide spend**, open the
wall with them, and (org admins) make and revoke TV links, below. The options are kept in
that browser; they become part of the link.

Options (combine them with `&`):

| Option | Effect |
| --- | --- |
| `?project=<name or id>` | Only that project |
| `?privacy=1` | The public view (below) |
| `?nospend=1` | No costs: the spend in Today and its average. Dash leaves them out of the data, not only off the screen |
| `?hours=8-18` | Work hours, for the off-hours summary (`22-6` wraps past midnight; `all` turns the summary off) |
| `?days=1-5` | Work days, 0 is Sunday (`1-5` is Monday to Friday) |
| `?token=<wall token>` | Open it without signing in (below) |

`?nospend=1` is for a screen where spend is sensitive. Anyone who can edit the link can remove
it; for a screen outside the organization, use a public wall token.

## A link for a TV

A TV shouldn't keep someone's session. An org admin makes a **wall token** instead: it
opens `/wall` (and its data, `/api/wall`, project icons, `/api/wall/icon`, and members' pictures,
`/api/wall/avatar`) for that organization and nothing else, not the API and not the dashboard. Make one in **Organization → Wall view → Links for TVs**, or with the API:

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

The public view shows only what anyone may see: the overall status, the counts in each stage,
timers and typical times, lines changed, runner slots, Claude usage, merges and the week's summary.
It hides task titles and prompts (in Just landed too), project names (they become "Project A", "Project B"…, lettered by when the
project was made) and icons, runner names ("Runner A"…), why a task failed, why it was sent back
(only "merge conflict" or "failing tests"), activity lines, issue titles, who should act on a
review or failure (names and pictures) and costs. With
it, `?project=` takes a project's id only, so names can't be guessed.

Open it with `?privacy=1`, or make a **public wall token**: its wall is always the public
view, whatever its link says, so it is the one to put on a screen outside the office or
share outside the organization.

```sh
curl -b cookies.txt -X POST https://dash.example.com/api/wall/tokens -d '{"name":"Lobby","public":true}'
```

## What it doesn't show yet

"Spend" is the cost of the Claude runs started today. Tasks from before Dash kept a record of
each run count their whole cost.
"First-try merges" are today's merges that took a single Claude run. Time from queue to
merge isn't recorded, so it isn't shown.
Who should act is a review's author (who created the task) and, for a failed task, who started
its latest run (else who created it). Dash has no assigned reviewers.
Typical run times come from runs Dash timed; until a project has some, they come from the runs
of its tasks in Review or failed.
