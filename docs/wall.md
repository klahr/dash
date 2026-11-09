# Wall view

A read-only, full-screen dashboard of an organization's work for a TV in the office, at
`/wall`. It is laid out for 1920×1080 and scales to fill any landscape screen (16:10, 4:3 and
ultra-wide screens are filled, not letterboxed). Narrow or portrait screens (a laptop window, a
portrait TV, a phone) get a stacked layout that scrolls. Nothing on it needs a click.

From top to bottom: whether the queue runs and how many runner slots are busy, the Claude
usage of the 5-hour and 7-day windows (with a marker where the queue pauses), a clock; the
number of tasks in each stage; a card per running task (elapsed time, run, cost so far,
lines changed, the tool call it is making); what needs a human (reviews oldest first, failed
tasks, open issues; red when waiting too long); the runners; today's merges, spend and
merges per hour; and a ticker of what happened lately.

It refreshes every 5 seconds.

## Opening it

Signed in, open `https://<your dash>/wall`: it shows your active organization, across all
of its projects.

**Wall view** in the account menu configures it: pick a project and privacy mode, open the
wall with them, and (org admins) make and revoke TV links, below. The options are kept in
that browser; they become part of the link.

Options (combine them with `&`):

| Option | Effect |
| --- | --- |
| `?project=<name or id>` | Only that project |
| `?privacy=1` | No task titles, prompts, issue titles, activity lines or costs |
| `?token=<wall token>` | Open it without signing in (below) |

## A link for a TV

A TV shouldn't keep someone's session. An org admin makes a **wall token** instead: it
opens `/wall` (and its data, `/api/wall`) for that organization and nothing else, not the
API and not the dashboard. Make one in **Wall view → Links for TVs**, or with the API:

```sh
# make one (as an org admin, signed in; the secret is shown only now)
curl -b cookies.txt -X POST https://dash.example.com/api/wall/tokens -d '{"name":"Office TV"}'
# → {"token":{"id":3,…},"secret":"dashw_…","path":"/wall?token=dashw_…"}

curl -b cookies.txt https://dash.example.com/api/wall/tokens            # list them
curl -b cookies.txt -X DELETE https://dash.example.com/api/wall/tokens/3 # revoke one
```

Open `https://dash.example.com/wall?token=dashw_…` on the TV. Anyone with the link sees
the wall, so treat it like a password; add `&privacy=1` where visitors walk past.

## What it doesn't show yet

"Spend" is the cost of the tasks worked on today (Dash keeps cost per task, not per run).
"First-try merges" are today's merges that took a single Claude run. Time from queue to
merge isn't recorded, so it isn't shown. The activity line comes from the events this Dash
saw since it started.
