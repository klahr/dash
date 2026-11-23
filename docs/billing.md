# Billing (Stripe, per seat)

If you host Dash for other people, you can charge organizations per member with a Stripe
subscription. Billing is **off unless you configure Stripe**: a self-hosted Dash has no
limits, no billing page and no banners, and behaves exactly as before.

## The rule

- A **personal workspace** (see [signup](signup.md#personal-workspaces)), and any
  organization with **one member**, is an **individual**. Individuals are free by default.
  With an individual price set, they are asked to pay, and anyone who can't clicks
  **I can't pay right now** and keeps using Dash for free (see [Individuals](#individuals)).
  Nothing is ever locked for an individual.
- An organization with **two or more members** needs an **active or trialing**
  subscription. Its quantity is the number of members ("seats"); pending invitations don't
  count.
- The instance's own **Default** organization (the `DASH_USERNAME` account's) is never billed.
- A platform admin can make any organization free, forever or until a date (see
  [Free organizations](#free-organizations)).

Prices and currencies live in Stripe, not in Dash: Dash only knows the ids of a recurring
per-seat price (and, if you want, an individual price), and Stripe's Checkout and invoices
show the amount.

## Setting it up

1. In Stripe, create a **product** with a **recurring price per unit** (per seat), monthly
   or yearly, in the currency you want. Note its id, `price_…`.
2. Turn on **Stripe Tax** (Settings → Tax) and register where you collect tax. Dash creates
   Checkout sessions with automatic tax and tax ID collection, so EU VAT is worked out from
   the customer's address, and businesses that give a valid VAT number in another EU country
   are charged with reverse charge. Set the price's tax behaviour (inclusive or exclusive) in
   Stripe.
3. Turn on the **Customer portal** (Settings → Billing → Customer portal): let customers update
   payment methods, see invoices and cancel. Leave "change quantity" off: Dash sets the seats.
4. Add a **webhook endpoint** at `https://<your Dash>/api/billing/webhook` (Developers →
   Webhooks) with these events:
   - `checkout.session.completed`
   - `customer.subscription.created`, `customer.subscription.updated`,
     `customer.subscription.deleted`
   - optionally `customer.subscription.paused`, `customer.subscription.resumed`

   Note its **signing secret**, `whsec_…`.
5. Configure Dash and restart:

| Variable                  | Flag                     | What                                                        |
|---------------------------|--------------------------|-------------------------------------------------------------|
| `STRIPE_SECRET_KEY`       | `-stripe-secret-key`     | the secret key (`sk_…`, or a restricted key `rk_…`)          |
| `STRIPE_WEBHOOK_SECRET`   | `-stripe-webhook-secret` | the webhook endpoint's signing secret (`whsec_…`)           |
| `STRIPE_PRICE_ID`         | `-stripe-price`          | the per-seat recurring price (`price_…`)                    |
| `STRIPE_INDIVIDUAL_PRICE_ID` | `-stripe-individual-price` | optional: the individual recurring price (`price_…`), see [Individuals](#individuals) |
| `DASH_BILLING_TRIAL_DAYS` | `-billing-trial-days`    | trial of an org's first subscription, in days (default 14; 0 = none) |
| `DASH_BILLING_GRACE`      | `-billing-grace`         | how long an org may go unpaid before its board becomes read-only (default `72h`) |
| `DASH_PUBLIC_URL`         | `-public-url`            | where Stripe sends people back to after Checkout and the portal (required) |

Keep the secrets in `.env` or the service's environment rather than on the command line.
A restricted key needs write access to Customers, Checkout Sessions, Customer portal and
Subscriptions.

On start, Dash logs `billing on` with the trial, the grace period and the webhook URL. If
Stripe is configured only in part (a variable missing, a product id instead of a price id,
no public URL), it logs `billing stays off` with the reason and runs without billing. With no
Stripe variables at all it says nothing.

Use test-mode keys (`sk_test_…`) and the Stripe CLI to try it out:
`stripe listen --forward-to localhost:7788/api/billing/webhook` prints a `whsec_…` to use as
`STRIPE_WEBHOOK_SECRET` while it runs.

## What people see

- **Organization → Billing** (owners only): the subscription's state, seats and renewal or
  trial end, with **Start trial** / **Subscribe** (Stripe Checkout) and **Manage billing**
  (the Stripe customer portal: card, invoices, VAT number, cancelling). Dash has no payment
  forms of its own.
- **Inviting a second member** into an unpaid organization offers the owner to start the
  trial straight away. The invitation is made either way.
- A **banner** on the board when an organization needs a subscription: until when it can keep
  working, or that the board is read-only. Owners get the buttons; other members are asked to
  talk to an owner.

## Individuals

Without `STRIPE_INDIVIDUAL_PRICE_ID`, individuals are free and never asked to pay.

With it, individuals are **paid by default, free on request**:

- An individual that has never subscribed and hasn't said it can't pay is **asked**: in
  **Organization → Billing**, and in a prompt when the owner opens the app (at most once a
  day in a browser). Both offer **Subscribe** (Stripe Checkout, one at the individual price,
  no trial) and **I can't pay right now**.
- **I can't pay right now** is one click and needs no reason. Dash remembers it for the
  workspace and stops asking. **Subscribe** stays in **Organization → Billing** for when they
  can.
- Asking never blocks. Closing the prompt changes nothing, and it comes back another day.
  Individuals have no grace period, no read-only board and no limits, whether they pay or not.
- A subscribed individual sees **Subscribed** in **Organization → Billing**, and **Manage
  billing** opens the customer portal to change the card or cancel. The subscription stays at
  one: it doesn't follow the members.
- After cancelling, they are free again and aren't asked afresh: anyone who has subscribed
  once has answered.
- The instance's **Default** organization, and an organization a platform admin made free,
  are never asked.

When an individual's organization **grows to two or more members**, the team rule applies:

- A live individual subscription moves to the per-seat price, with a seat per member
  (prorated, like any seat change). Nothing for the owner to do.
- An organization that chose free, or never answered, needs a team subscription like any
  other, after the usual grace period. Inviting the second member offers the trial.

To set it up:

1. In Stripe, add a second **recurring price** (per month or year, not per unit) to the
   product, for individuals. Note its id, `price_…`.
2. Set `STRIPE_INDIVIDUAL_PRICE_ID` to it, and `DASH_INDIVIDUAL_PRICE` to the price as it
   should read, e.g. `€5 / month` (see [branding](branding.md)). Restart.

   ```sh
   STRIPE_INDIVIDUAL_PRICE_ID=price_...
   DASH_INDIVIDUAL_PRICE="€5 / month"
   ```

Dash refuses an individual price that isn't a `price_…` id, or is the per-seat price, and
logs `billing stays off` with the reason.

## Free organizations

A **platform admin** can make an organization free, forever or until a date: a partner, a
pilot, your own team. While it holds, the organization needs no subscription, whatever its
number of members: no banner, no read-only board, no prompt to subscribe when someone invites.

1. Switch to the organization. For one you aren't a member of, use the
   [API](#api) instead.
2. Open **Organization → Billing**. Platform admins see **Free override (platform admins)**
   there.
3. Pick **Free forever**, or **Free until** and the last free day. Add a note on why if you
   like: only platform admins see it, with who set the override and when.
4. Click **Make free**. **Clear** ends it.

Members and owners see "Free forever" or "Free until <date>" in **Organization → Billing**
instead of the subscribe button.

When a date passes, or you clear the override, the usual rule applies again. If the
organization then needs a subscription, it gets a full grace period from that moment: an
earlier, unpaid grace period doesn't count.

An override doesn't touch Stripe. A subscription the organization has carries on and is billed
as before, and its seats keep following the members. If the owner doesn't want to pay while
the organization is free, they cancel it in **Manage billing**.

## Seats

Seats follow the members. When someone accepts an invitation (with an account they have, or a
new one), leaves or is removed, Dash sets the subscription's quantity to the new member count
with `proration_behavior=create_prorations`: Stripe credits or charges the difference on the
next invoice. If Stripe can't be reached, the error is logged and the next change, or the
next subscription event, corrects it. A subscription that Checkout created with fewer seats
than members (someone joined while the owner was paying) is corrected as soon as its event
arrives.

An organization that goes back to one member is free again, but its subscription isn't
cancelled automatically (it is billed for one seat): the owner cancels it in the portal.

An [individual](#individuals) subscription isn't seat-synced: it stays at one while the
organization has one member, and moves to the per-seat price once it has two.

## When an organization doesn't pay

An organization needs a subscription from the moment it has two members, and it has a grace
period (`DASH_BILLING_GRACE`, 3 days by default) from then to start one, or from when an
existing one stopped being active or trialing: a failed payment (`past_due`, while Stripe
retries), a trial that ended without a card, a cancelled subscription. After the grace period:

- the **board is read-only**: everything can still be read, but the API answers changes to
  projects, tasks, scans, files and processes with `402` and
  `{"code": "billing_read_only"}`; the chat and the board's MCP tools refuse to change
  anything too;
- its **queued tasks don't start**, here or on its runners: they stay queued, saying why;
  scheduled scans wait too. Tasks already running finish;
- owners can still manage members and invitations, tokens and billing. Removing members down
  to one makes the organization free again at once.

**Nothing is deleted.** As soon as the subscription is active or trialing again (the webhook
arrives), the board is writable and queued tasks start.

## Webhooks

Every request to `/api/billing/webhook` must carry a valid `Stripe-Signature` (HMAC-SHA256 with
the signing secret, timestamp within 5 minutes); anything else gets `400`. Each event is
applied once: its id is recorded, and a delivery Stripe repeats is acknowledged without being
applied again. An event that fails to apply is forgotten and answered `500`, so Stripe retries
it. Events are applied in order of their creation time, so a late, older event can't undo a
newer one, and an old subscription ending doesn't override a newer one that is active.

Dash finds the organization from the subscription's `org_id` metadata (set by Checkout) or its
customer, and ignores customers and subscriptions that aren't an organization's, so the same
Stripe account can sell other things too.

For each organization Dash keeps, in SQLite (`org_billing`): the Stripe customer and
subscription ids, the subscription's status, seats, period and trial end, whether it cancels
at the period end, whether the org has had its trial, since when it has been unpaid, and a
platform admin's free override. In `org_billing_individual` it keeps the subscription's
price and when an individual chose **I can't pay right now**.

## API

| Method and path                              | Who            | What                                     |
|----------------------------------------------|----------------|------------------------------------------|
| `GET /api/orgs/{id}/billing`                 | members        | the state: `enabled`, `free`, `free_forever`, `free_until`, `required`, `status`, `read_only`, `grace_until`, `seats`, `period_end`, `trial_end`, `can_subscribe`, `can_manage`, and for individuals `individual`, `individual_price`, `ask`, `chose_free`, `plan`, … |
| `POST /api/orgs/{id}/billing/checkout`       | owners         | `{url}` of a Stripe Checkout session (an individual's is at the individual price) |
| `POST /api/orgs/{id}/billing/free`           | owners         | an individual can't pay right now: stop asking; answers the state (`409` for a team) |
| `POST /api/orgs/{id}/billing/portal`         | owners         | `{url}` of a customer portal session     |
| `POST /api/billing/webhook`                  | Stripe         | events, signed                           |
| `GET /api/admin/orgs/{id}/billing`           | platform admins | the override (`free_forever`, `free_until`, `note`, `set_by`, `set_at`, `active`) and the `state` |
| `PUT /api/admin/orgs/{id}/billing`           | platform admins | `{free_forever}` or `{free_until}` (RFC 3339, in the future), with an optional `note` |
| `DELETE /api/admin/orgs/{id}/billing`        | platform admins | clear the override                       |

They need a browser session; API tokens get `403`, and so does anyone but a platform admin on
`/api/admin/…`, owners of the organization included. For example, signed in as a platform
admin (with the browser's session cookie):

```sh
curl -X PUT https://dash.example.com/api/admin/orgs/42/billing \
  -H 'Content-Type: application/json' -b '__Host-dash_session=…' \
  -d '{"free_until": "2027-01-01T00:00:00Z", "note": "pilot"}'
``` With billing off, `GET` answers
`{"enabled": false}` and the others `404`. `POST /api/orgs/{id}/invitations` adds
`"billing": {"subscription_needed": true, …}` to its answer when the org would need a
subscription it doesn't have.
