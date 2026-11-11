# Notifications

Dash can tell you when something needs you, not just in the open browser tab: by **push
notifications** to your browser or installed app, and by **webhooks** to other services.

## Push notifications

Turn on **Preferences → Notifications** in each browser that should notify you. While Dash is
open in the background, the page notifies you itself; where the browser supports Web Push
(Chrome, Edge, Firefox, Safari on macOS, and the installed app on iOS 16.4 or later), the hub
also pushes these to the browser, so they arrive while Dash is closed:

- a task is ready for review
- a task failed
- Claude waits for you to approve a tool, with **Approve** and **Deny** buttons where the
  system shows them (they work for 15 minutes, until the request is answered, or until Dash
  restarts)
- GitHub Actions starts failing on the default branch

Tapping a notification opens the task. **Send a test**, under the switch, pushes one right away.
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

Each project has its own webhooks: **Project settings → Notification webhooks → Add webhook**.
The whole org shares them.

You can also have **personal webhooks**: **account menu → My webhooks → Add webhook**. A
personal webhook gets the events of every project in the org you made it in, for as long as you
are a member of that org. It is yours alone: other members, org admins included, don't see
it. If a project's webhook and a personal one point to the same URL, each event is sent there
once. A personal webhook's test notification names no project.

A webhook has a URL, a format, the events it sends and an on/off switch. **Send test** posts a
test notification right away (even to a paused webhook). The list shows how each webhook's last
delivery went, and **History** lists its last 50 deliveries, newest first: when it was sent, the
event, its project and task, whether it went through, the HTTP status, how many tries it took
and how long, and the error if it failed. Deleting a webhook deletes its history too.

### Events

| Event               | When                                                         |
| ------------------- | ------------------------------------------------------------ |
| `task.review`       | a task is ready for review                                   |
| `task.failed`       | a task failed                                                |
| `task.merged`       | a task's work landed                                         |
| `permission.needed` | Claude waits for you to approve a tool                       |
| `build.failing`     | GitHub Actions starts failing on the default branch          |
| `checkout.behind`   | a runner's checkout was left behind origin (off by default)  |
| `webhook.test`      | the Send test button (always sent, whatever is ticked)       |

These are the moments the web app notifies you of. Links point back to the task in Dash
(`/?project=<id>&task=<id>`); set `-public-url` (or `DASH_PUBLIC_URL`) so they use the address
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
    "url": "https://dash.example.com/?project=a1b2c3d4e5f6&task=0f9e8d7c6b5a"
  },
  "message": "tests failed"
}
```

`permission.needed` events also have `"permission": {"id", "tool"}`, and `build.failing`
events have `"build": {"branch", "sha", "url"}` (`task` is the task investigating the failure,
if there is one). `checkout.behind` events say in `message` which runners' checkouts of the
project another runner's push left behind, and why: for a clone the runner adopted, uncommitted
changes or local commits; for one it made itself, which always follows origin (saving what is in
the way on a `dash-backup/` branch first), why syncing failed. A clean checkout follows origin by
itself. New webhooks don't subscribe to it; each runner's
checkout notifies at most once every 6 hours. The headers `Dash-Event` and `Dash-Delivery` repeat the type and id; the id
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

### API

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

Personal webhooks need a person: a signed-in session or one of your API tokens.

Runners (`dash runner`) send no notifications. Their hub sends them for the tasks it gives
the runners.
