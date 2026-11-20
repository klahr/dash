# Notifications

Dash can tell you when something needs you, not just in the open browser tab: by **push
notifications** to your browser or installed app, by **webhooks** to other services, and by a
daily or weekly **summary** by email or webhook.

## Your notification settings

**Account → Notifications** holds all of your own notifications, in one place:

- [whose tasks](#whose-tasks) you hear about: **Tasks I created** or **All tasks**
- [Planning cards](#planning-cards): when you're mentioned or added to a card
- **Summary**: off, daily or weekly, when, of which projects, where it goes and what is in it
  ([summary](#summary))
- **Push**: **Push to this browser**, and which events [push](#push-notifications) to your browsers
- **Webhooks**: your [personal webhooks](#notification-webhooks). Add, test and change them
  here. A webhook gets each event the moment it happens.

Dash keeps your settings in the organization you are in, so they apply on every device. Each
organization you are in has its own. **Push to this browser** is the one setting of the browser
you are in. A project's own webhooks are in **Project settings → Webhooks**.

## Whose tasks

On a shared board, a task's news goes to the person who created it, so someone answers for it.
Choose under **Account → Notifications → Tasks**:

- **Tasks I created** (the default): a task's pushes and summary items reach you only if you
  created it.
- **All tasks**: you hear about every task in the org, as before.

This covers a task ready for review, failed, merged, or waiting for you to approve a tool.
A task no person is behind reaches everyone who wants that event, whatever they chose: one Dash
made (from an issue or an automation), one whose creator's account was deleted, and one whose
creator left the organization.

A [planner](users.md#planners) can't open tasks. A task a planner started with **Start** on a
card goes to the card's members who aren't planners, as if they had created it. With none on
the card, it reaches everyone, like a task Dash made. Planners never get task notifications.

Events about the whole org aren't anyone's task, and still reach everyone who turned them on: a
failing build on the default branch, a failed deploy, a runner's disk filling up, a checkout left
behind origin.
Goals go to their owner, as before. [Webhooks](#notification-webhooks) don't follow this choice:
each one has its own events.

> **Changed in this version.** Task events used to push to every member who turned them on.
> Now they go to the task's creator. If you want every task's news, choose **All tasks**.

## Planning cards

You hear about a [Planning](planning.md) card when:

- someone mentions you in a comment on it: `@` and your username, such as `@pia`
- someone adds you to its **Members**
- you're a planner on the card, or the planner who started its task, and the task is ready for
  review, failed or merged. Others on the card hear of the task itself (see
  [Whose tasks](#whose-tasks)).

You don't hear about what you did yourself. These are pushed to your browsers only: they aren't
in the summary, and webhooks don't get them. Turn them off under **Account → Notifications →
Push → Planning: you're mentioned or added to a card**. Tapping one opens the card.

Planners have **Account → Notifications** too, with **Push to this browser** and this one
choice. Card news is the only notification a planner gets: there is no summary for planners.

## Push notifications

Turn on **Account → Notifications → Push to this browser** in each browser that should notify you. While Dash
is open in the background, the page notifies you itself; where the browser supports Web Push
(Chrome, Edge, Firefox, Safari on macOS, and the installed app on iOS 16.4 or later), the hub
also pushes these to the browser, so they arrive while Dash is closed. Choose which under
**Account → Notifications → Push**:

- a task is ready for review ([whose tasks](#whose-tasks))
- a task failed ([whose tasks](#whose-tasks))
- Claude waits for you to approve a tool ([whose tasks](#whose-tasks)), with **Approve** and **Deny** buttons where the
  system shows them (they work for 15 minutes, until the request is answered, or until Dash
  restarts)
- a [goal](goals.md) waits for you, is done or ran out of budget
- GitHub Actions starts failing on the default branch
- a [deploy](deploys.md#when-a-deploy-fails) fails
- a runner's disk is nearly full ([Disk space on a runner](several-computers.md#disk-space-on-a-runner))
- a scan or Claude finds a new issue (off until you tick it)
- you're mentioned or added to a [Planning card](#planning-cards), or its work needs a planner

Tapping a notification opens the task, the goal on the **Goals** page, the issue on the
**Issues** page, the deploy on the **Deploys** page, or the card on **Planning**. **Send a test**, under the switch, pushes one right away.
On an iPhone or iPad, add Dash to the home screen first ([phone.md](phone.md)) and turn
notifications on in the installed app: Safari tabs can't get pushes there.

A browser gets the notifications of every project in the org it was in when you turned them on
(open Dash in another org to move it there), for as long as you are a member. Turning
notifications off, or logging out, unsubscribes it.

Pushes are encrypted for the browser (RFC 8291) and signed with the hub's own key (VAPID,
RFC 8292), which Dash makes on first start and keeps in its database: the browser vendor's push
service sees neither the content nor can it send in your hub's name. Dash needs no setup for
them, only outgoing HTTPS to the push services (`fcm.googleapis.com`, `*.push.services.mozilla.com`,
`*.push.apple.com`, …). They aren't sent to private addresses on a hub that doesn't allow those
for webhooks (see [Private addresses](#private-addresses)). Subscriptions the browser drops are
deleted at the next push. Runners don't push; their hub does.

## Notification webhooks

Each project has its own webhooks: **Project settings → Webhooks → Add webhook…**.
The whole org shares them.

You can also have **personal webhooks**: **Account → Notifications → Webhooks → Add webhook…**. A
personal webhook gets the events of every project in the org you made it in, for as long as you
are a member of that org. It is yours alone: other members, org admins included, don't see
it. If a project's webhook and a personal one point to the same URL, each event is sent there
once. A personal webhook's test notification names no project.

A webhook has a URL, a format, the events it sends and an **Enabled** switch. **Send test** posts a
test notification right away (even to a paused webhook). The list shows how each webhook's last
delivery went, and **History** lists its last 50 deliveries, newest first: when it was sent, the
event, its project and task, whether it went through, the HTTP status, how many tries it took
and how long, and the error if it failed. Deleting a webhook deletes its history too.

### Events

| Event               | When                                                         |
| ------------------- | ------------------------------------------------------------ |
| `task.review`       | a task is ready for review                                   |
| `task.failed`       | a task failed                                                |
| `task.merged`       | a task's work landed, or it was closed with no changes       |
| `permission.needed` | Claude waits for you to approve a tool                       |
| `goal.attention`    | a [goal](goals.md) waits for you, is done or out of budget   |
| `issue.new`         | a [scan](scans.md) or Claude (`report_issue`) finds a new issue (off by default) |
| `build.failing`     | GitHub Actions starts failing on the default branch          |
| `deploy.failed`     | a [deploy](deploys.md#when-a-deploy-fails) or rollback failed |
| `runner.disk`       | a runner that works on the project is nearly out of disk space |
| `checkout.behind`   | a runner's checkout was left behind origin (off by default)  |
| `webhook.test`      | the Send test button (always sent, whatever is ticked)       |
| `summary`           | your [summary](#summary), to the webhooks you chose for it    |

These are the moments the web app notifies you of. Links point back to the task in Dash
(`/p/<project>/tasks/<id>`, see [Links](links.md)); set `-public-url` (or `DASH_PUBLIC_URL`) so they use the address
people reach Dash at. With org subdomains, links use the project's org subdomain.

### Formats

**Slack**: create an incoming webhook in a Slack app and paste its URL. Each event is one short
line with an emoji and a link to the task.

**ntfy**: paste a topic URL, e.g. `https://ntfy.sh/my-dash-topic` (pick a hard-to-guess topic
name on the public server). Failures and approval requests arrive with high priority, and
tapping a notification opens the task. For protected or self-hosted servers, add an access
token: it is sent as `Authorization: Bearer <token>`. Approval requests get **Approve** and
**Deny** buttons that answer Claude straight from your phone. The buttons work for 15 minutes,
until the request is answered, or until Dash restarts, and they need `-public-url` to be an
address your phone can reach.

**JSON**: a `POST` with a JSON body to your own service:

```json
{
  "id": "evt_5f0c…",
  "type": "task.failed",
  "timestamp": "2026-11-08T12:00:00Z",
  "created": 1794139200,
  "project": { "id": "a1b2c3d4e5f6", "name": "shop" },
  "task": {
    "id": "0f9e8d7c6b5a",
    "title": "Fix checkout rounding",
    "status": "failed",
    "url": "https://dash.example.com/p/a1b2c3d4e5f6/tasks/0f9e8d7c6b5a"
  },
  "message": "tests failed"
}
```

`issue.new` events have `"issue": {"id", "title", "severity", "url"}` instead of `task`, and
the issue's file in `message`. They fire once per issue: an issue reported again (the same
type, file and rule or title) doesn't fire, nor does one you add by hand. Existing webhooks
don't get them until you tick the event; neither do new ones.
`goal.attention` events have `"goal": {"id", "title", "status", "url"}` instead of `task`, and say
in `message` what the goal waits for, or why it ended; existing webhooks don't get them until you
tick the event. `permission.needed` events also have `"permission": {"id", "tool"}`, and `build.failing`
events have `"build": {"branch", "sha", "url"}` (`task` is the task investigating the failure,
if there is one). `deploy.failed` events have `"deploy": {"id", "environment", "commit", "rollback", "url"}`
instead of `task`, and say in `message` why it failed. `checkout.behind` events say in `message` which runners' checkouts of the
project another runner's push left behind, and why: for a clone the runner adopted, uncommitted
changes or local commits; for one it made itself, which always follows origin (saving what is in
the way on a `dash-backup/` branch first), why syncing failed. A clean checkout follows origin by
itself. New webhooks don't subscribe to it; each runner's
checkout notifies at most once every 6 hours. `runner.disk` events have `"runner": {"id", "name"}`
and say in `message` how much space is left and which folders take the most; they go to the
webhooks of every project the runner works on (each URL once), at most once every 6 hours per
runner. The headers `Dash-Event` and `Dash-Delivery` repeat the type and id; the id
stays the same when a delivery is retried, so you can drop duplicates.

#### Verifying the signature

Dash shows a JSON webhook's signing secret (`whsec_…`) once, when you create it. **Rotate
secret** replaces it, and the old one stops working at once. Each delivery is signed the way
Stripe signs its webhooks:

```
Dash-Signature: t=1794139200,v1=5257a869e7ecebeda32affa62cdca3fa51cad7e77a0e56ff536d0ce8e108d8bd
```

`v1` is the hex HMAC-SHA256 of `<t>.<raw request body>` with the secret. Recompute it,
compare in constant time, and refuse requests whose `t` is more than a few minutes from now.
In Go, `notify.Verify(body, header, secret, time.Now(), 5*time.Minute)` does all of this.

### Delivery

Deliveries run in the background, so a slow or unreachable service never holds up the queue.
Each attempt has a 10 second timeout. Network errors, 5xx and 429 answers are retried with
exponential backoff (4 attempts over about 15 seconds). Other answers, and redirects, count
as failures right away. Failures are logged with the webhook's id and host, but never its URL,
secret or token (Slack and ntfy URLs are secrets themselves).

### Private addresses

On a hosted Dash, an internet-facing one (`-public-url` or `-trust-proxy`), or any Dash with open signup, webhooks may not reach private, loopback,
link-local or carrier-grade NAT addresses. This is checked when you save the URL and again on every connection, after DNS,
so internal services aren't reachable through a webhook. An invite-only self-hosted Dash
(no Stripe billing, no open signup, not internet-facing) allows these addresses, so you can notify a service on your own network.
`-webhook-private=on|off` (or `DASH_WEBHOOK_PRIVATE`) overrides the default; `on` on an internet-facing Dash logs an
`UNSAFE` warning at every start.

## Summary

A summary tells you, in one message, what happened since the last one. Set it up in
**Account → Notifications**. Each change saves as you make it. A daily or weekly summary saves
once it has a time, somewhere to go and its projects; until then, the field still missing says so.

- **Summary**: off (the default), daily or weekly, and the time to send it. The time is in your
  time zone, which Dash takes from your browser when the settings save, and stays at that time when the
  clocks change.
- **Projects**: all of the organization's projects (new ones too), or the ones you pick.
- **Send it to**: email, one or more of your personal webhooks, or both. Pick at least one.
  Email goes to your account's address, shown beside it; it is greyed out when this Dash has no
  [SMTP](signup.md#email-smtp) set up.
- **What goes in it**: new issues (from the severity you choose up), tasks ready for review,
  failed tasks, merged tasks, your goals waiting for you, and failing builds. Task items
  follow [whose tasks](#whose-tasks) you chose.

**Send the summary now** saves the page and sends what happened since the last summary to
each destination right away, and says how each went.

Nothing to report sends nothing. A summary covers the time since the last one, so after a
restart nothing is sent twice or left out. It only covers projects of organizations you are
still a member of. If one destination fails, the others still get it; a webhook's failure
shows in its **History**. Deleting a webhook takes it off the summary's destinations, and a
paused webhook gets no summary.

Summaries are sent by the hub, never by runners.

### By email

One plain-text email, grouped by project, with a link to each item. At the end, **Stop these
summaries** turns the summary off without signing in; mail apps that offer an unsubscribe
button use the same link (`List-Unsubscribe`).

### By webhook

A webhook chosen as a destination gets the `summary` event, whatever events it has ticked. It
is signed, retried and recorded like every other delivery.

**Slack**: one message, with the counts and a short line per item, grouped by project. A long
summary stops after 40 items (or about 3,500 characters) with "and N more" and a link to Dash.

**ntfy**: the counts as the title, the items as the body, cut the same way. Tapping it opens Dash.

**JSON**:

```json
{
  "id": "evt_7a1b…",
  "type": "summary",
  "timestamp": "2026-11-18T07:00:00Z",
  "created": 1794985200,
  "message": "Summary: 2 new issues, 1 failed",
  "summary": {
    "period_start": "2026-11-17T07:00:00Z",
    "period_end": "2026-11-18T07:00:00Z",
    "url": "https://dash.example.com/",
    "projects": [
      {
        "id": "a1b2c3d4e5f6",
        "name": "shop",
        "url": "https://dash.example.com/p/a1b2c3d4e5f6",
        "items": [
          { "kind": "issue", "title": "SQL injection in search", "severity": "high",
            "url": "https://dash.example.com/p/a1b2c3d4e5f6/issues/9f8e7d6c5b4a", "at": "2026-11-17T15:02:11Z" },
          { "kind": "failed", "title": "Fix checkout rounding",
            "url": "https://dash.example.com/p/a1b2c3d4e5f6/tasks/0f9e8d7c6b5a", "at": "2026-11-17T16:40:00Z" }
        ]
      }
    ]
  }
}
```

An item's `kind` is `issue`, `review`, `failed`, `merged`, `goal` or `build`. A summary has no
`project`.

## API

| Request                              | What                                                        |
| ------------------------------------ | ----------------------------------------------------------- |
| `GET /api/projects/{pid}/webhooks`   | a project's webhooks (never the secrets or tokens)          |
| `POST /api/projects/{pid}/webhooks`  | add one; the answer has its signing secret, shown only then |
| `GET /api/me/webhooks`               | your personal webhooks in your active org                   |
| `POST /api/me/webhooks`              | add a personal webhook                                      |
| `PATCH /api/webhooks/{id}`           | change one (`name`, `url`, `format`, `events`, `enabled`, `token`) |
| `DELETE /api/webhooks/{id}`          | delete one, with its history                                |
| `POST /api/webhooks/{id}/rotate`     | a new signing secret                                        |
| `POST /api/webhooks/{id}/test`       | send a test notification now                                |
| `GET /api/webhooks/{id}/deliveries`  | its last 50 deliveries, newest first                        |
| `GET /api/me/notifications`          | your notification settings in your active org               |
| `PUT /api/me/notifications`          | change them; fields left out keep their value               |
| `POST /api/me/notifications/test-summary` | send your summary now, to the destinations you chose   |

Personal webhooks and notification settings need a person: a signed-in session or one of
your API tokens.

Runners (`dash runner`) send no notifications. Their hub sends them for the tasks it gives
the runners.
