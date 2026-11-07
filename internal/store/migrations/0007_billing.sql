-- Per-seat billing with Stripe (off unless configured; see docs/billing.md).
-- One row per org that has been through billing: its Stripe customer and
-- subscription as the webhooks last described them. Orgs without a row have
-- never subscribed. Nothing here is ever needed to read an org's records:
-- billing only decides whether the board may change.

-- status is Stripe's subscription status ('' = no subscription yet).
-- period_end / trial_end are Unix seconds (0 = none). event_at is the
-- created time of the last subscription event applied, so an older event
-- arriving late can't undo a newer one. unpaid_since is when the org first
-- needed a subscription it didn't have (0 = it has one, or needs none): the
-- grace period counts from there. trial_used: the org has had a trial, so
-- Checkout doesn't offer another.
CREATE TABLE org_billing (
	org_id               INTEGER PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
	customer_id          TEXT NOT NULL DEFAULT '',
	subscription_id      TEXT NOT NULL DEFAULT '',
	subscription_item_id TEXT NOT NULL DEFAULT '',
	status               TEXT NOT NULL DEFAULT '',
	quantity             INTEGER NOT NULL DEFAULT 0,
	period_end           INTEGER NOT NULL DEFAULT 0,
	trial_end            INTEGER NOT NULL DEFAULT 0,
	cancel_at_period_end INTEGER NOT NULL DEFAULT 0,
	trial_used           INTEGER NOT NULL DEFAULT 0,
	unpaid_since         INTEGER NOT NULL DEFAULT 0,
	event_at             INTEGER NOT NULL DEFAULT 0,
	updated_at           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX org_billing_customer ON org_billing (customer_id) WHERE customer_id <> '';

-- Stripe webhook events already handled, by event id: Stripe delivers each
-- at least once, and retries until it gets a 2xx.
CREATE TABLE stripe_events (
	id          TEXT PRIMARY KEY,
	received_at INTEGER NOT NULL
);
