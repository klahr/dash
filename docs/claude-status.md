# Claude status

Dash can follow Claude's status page, [status.claude.com](https://status.claude.com). When
Claude has an incident, everyone on this Dash sees it beside the board, in every organization.
Tasks failing or running slowly at that time usually have that cause.

A platform admin turns it on. It is off until then.

## Turn it on

1. Open **Account → Claude status**. Only platform admins see this section.
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
page instead. In **Account → Claude status**, turn on **Also check the status page every 5
minutes**. This also catches updates a webhook missed.

## What people see

- **The Claude pill** in the sidebar (in the menu on a phone) shows while Claude has an
  incident or a component isn't operational, for example "Claude: major incident". It is red
  for partial and major outages, and amber for minor ones and maintenance.
- **Click the pill** to see the affected components, the active incidents with each update,
  and the incidents resolved in the last week. **Status page** opens status.claude.com.
- **A notice** pops up when an incident starts and when it is resolved.
- The command palette has **Claude status** too.

Dash keeps resolved incidents for 30 days.

## A new webhook URL

If the URL leaked, choose **New URL…** in **Account → Claude status**. The old URL stops
working at once. Subscribe the new URL on the status page, and unsubscribe the old one.

## Another status page

**Status page** in the same section takes any Atlassian Statuspage address, for example
`https://status.example.com`. Switching forgets the incidents heard from the old page.

## For scripts

| Call | Who | What |
| --- | --- | --- |
| `GET /api/claude-status` | anyone signed in | the status, active incidents, those resolved this week, components not operational |
| `GET /api/admin/claude-status` | platform admins | the settings, the webhook URL, what was heard recently |
| `PUT /api/admin/claude-status` | platform admins | `{"enabled": true, "poll": false, "page_url": "https://status.claude.com"}`, each optional |
| `POST /api/admin/claude-status/rotate` | platform admins | a new webhook URL |
| `POST /api/admin/claude-status/sync` | platform admins | read the status page now |
| `POST /api/claude-status/webhook/{token}` | the status page | its webhook, no sign-in: the token is the credential |
