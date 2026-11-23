-- Individuals (see docs/billing.md#individuals), one row per org that has
-- either: price_id is the price of the org's subscription as Stripe last
-- told it, so an individual subscription can be told from a per-seat one;
-- chose_free_at (Unix seconds; 0 = not chosen) is when an individual clicked
-- "I can't pay right now", which stops Dash asking them to pay.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS org_billing_individual (
	org_id        INTEGER PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
	price_id      TEXT NOT NULL DEFAULT '',
	chose_free_at INTEGER NOT NULL DEFAULT 0
);
