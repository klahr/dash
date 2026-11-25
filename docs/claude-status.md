# Claude status

Dash can follow Claude's status page, [status.claude.com](https://status.claude.com). When
Claude has an incident, everyone on this Dash sees it beside the board, in every organization.
Tasks failing or running slowly at that time usually have that cause.

A platform admin turns it on. It is off until then.

## Turn it on

1. Open **Organization → Claude status** (account menu → Organization, under **This Dash**). Only platform admins see this section.
2. Turn on **Show Claude's status**. Dash makes a webhook URL for it.
3. Copy the webhook URL. Anyone with it can post to it, so treat it like a password.
4. Open [status.claude.com](https://status.claude.com) and choose **Subscribe to updates**,
   then the webhook option. Paste the URL, give an email address for delivery failures, and
   subscribe.
5. Choose **Check now** to read the incidents from before you subscribed.

The status page must reach the webhook URL over the internet. Start Dash with `-public-url`
(or `DASH_PUBLIC_URL`) set to its public HTTPS address, so the URL names that address:

```sh
./bin/dash -public-url https://dash.example.com
```

See [Deploy the hub](deploy.md) for putting Dash on the internet.

## Without webhooks

A Dash the status page can't reach (behind a firewall, or only on `localhost`) can read the
page instead. In **Organization → Claude status**, turn on **Also check the status page every 5
minutes**. This also catches updates a webhook missed.

## Choose the components that count

The status page reports on many components, and most don't affect Dash's tasks. In
**Organization → Claude status → Components**, check the ones that matter. Each change is saved
at once, for everyone on this Dash.

Until you choose, **Claude Code**, **Claude API (api.anthropic.com)** and **claude.ai** count.
They are matched by name, so they work before Dash has read the page. A component the page
adds later is not checked. **Use the defaults** goes back to these three.

Choose **Check now** first to list all the page's components.

Only these count for the status icon and the notices:

- a checked component that isn't operational;
- an incident or maintenance that affects a checked component;
- an incident that names no component, so a general outage isn't missed.

The page's overall status covers every component, so it doesn't light the status icon by itself.

## What people see

- **The [status icon](status.md)** in the top bar shows a dot while something that counts is
  wrong: red for partial and major outages, amber for minor ones and maintenance. Its popover
  has a **Claude** row that counts the affected services, from the components and the
  incidents, each once: for example "3 services affected", or "Claude Code degraded
  performance" for one. An incident that names no service shows as "major incident".
- **Details** on that row shows the affected services, the active incidents with each update,
  and the incidents resolved in the last week. What doesn't count is folded under **Not
  counted**, so it is clear why the icon stays quiet. **Status page** opens status.claude.com.
- **A notice** pops up when an incident that counts starts and when it is resolved.
- The command palette has **Claude status** too.

Dash keeps resolved incidents for 30 days.

## A new webhook URL

If the URL leaked, choose **New URL…** in **Organization → Claude status**. The old URL stops
working at once. Subscribe the new URL on the status page, and unsubscribe the old one.

## Another status page

**Status page** in the same section takes any Atlassian Statuspage address, for example
`https://status.example.com`. Switching forgets the incidents heard from the old page.

## For scripts

| Call | Who | What |
| --- | --- | --- |
| `GET /api/claude-status` | anyone signed in | the status, active incidents, those resolved this week, components not operational; what doesn't count has `"ignored": true`, and `level` is the status over what counts |
| `GET /api/admin/claude-status` | platform admins | the settings, the webhook URL, what was heard recently, the components and which count |
| `PUT /api/admin/claude-status` | platform admins | `{"enabled": true, "poll": false, "page_url": "https://status.claude.com", "components": ["Claude Code"]}`, each optional; `components` takes ids or names; `{"default_components": true}` goes back to the defaults |
| `POST /api/admin/claude-status/rotate` | platform admins | a new webhook URL |
| `POST /api/admin/claude-status/sync` | platform admins | read the status page now |
| `POST /api/claude-status/webhook/{token}` | the status page | its webhook, no sign-in: the token is the credential |
