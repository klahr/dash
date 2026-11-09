# Notification webhooks

Dash can tell other services when something needs you, not just the open browser tab. Each
project has its own webhooks: **Project settings → Notification webhooks → Add webhook**.

A webhook has a URL, a format, the events it sends and an on/off switch. **Send test** posts a
test notification right away (even to a paused webhook) and the list shows how each webhook's
last delivery went.

## Events

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

## Formats

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
project another runner's push left behind, and why (uncommitted changes, local commits): a
clean checkout follows origin by itself. New webhooks don't subscribe to it; each runner's
checkout notifies at most once every 6 hours. The headers `Dash-Event` and `Dash-Delivery` repeat the type and id; the id
stays the same when a delivery is retried, so you can drop duplicates.

### Verifying the signature

Dash shows a JSON webhook's signing secret (`whsec_…`) once, when you create it. **Rotate
secret** replaces it, and the old one stops working at once. Each delivery is signed the way
Stripe signs its webhooks:

```
Dash-Signature: t=1794139200,v1=5257a869e7ecebeda32affa62cdca3fa51cad7e77a0e56ff536d0ce8e108d8bd
```

`v1` is the hex HMAC-SHA256 of `<t>.<raw request body>` with the secret. Recompute it,
compare in constant time, and refuse requests whose `t` is more than a few minutes from now.
In Go, `notify.Verify(body, header, secret, time.Now(), 5*time.Minute)` does all of this.

## Delivery

Deliveries run in the background, so a slow or unreachable service never holds up the queue.
Each attempt has a 10 second timeout. Network errors, 5xx and 429 answers are retried with
exponential backoff (4 attempts over about 15 seconds). Other answers, and redirects, count
as failures right away. Failures are logged with the webhook's id and host, but never its URL,
secret or token (Slack and ntfy URLs are secrets themselves).

## Private addresses

On a hosted Dash, or any Dash with open signup, webhooks may not reach private, loopback,
link-local or carrier-grade NAT addresses. This is checked when you save the URL and again on every connection, after DNS,
so internal services aren't reachable through a webhook. An invite-only self-hosted Dash
(no Stripe billing and no open signup) allows these addresses, so you can notify a service on your own network.
`-webhook-private=on|off` (or `DASH_WEBHOOK_PRIVATE`) overrides the default.

Runners (`dash runner`) send no notifications. Their hub sends them for the tasks it gives
the runners.
