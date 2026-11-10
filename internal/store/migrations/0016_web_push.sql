-- Web Push (internal/notify/webpush.go). The hub's VAPID key signs its
-- pushes; made once, on first start, and kept so browsers' subscriptions
-- (made for its public key) go on working. private_key is the P-256
-- scalar, base64url; like webhook secrets it is needed in the clear.
CREATE TABLE push_vapid (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	private_key TEXT NOT NULL,
	created_at  INTEGER NOT NULL
);

-- Browsers that get notifications by Web Push: one user's, in the org they
-- turned notifications on in (a push goes to the subscriptions of the
-- project's org whose user is still a member). A browser has one endpoint;
-- whoever signs in on it and turns notifications on takes it over.
-- endpoint, p256dh and auth are what pushing needs, and secrets: they never
-- leave Dash. The last_* columns describe the latest delivery. Times are
-- Unix seconds.
CREATE TABLE push_subscriptions (
	id         TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	endpoint   TEXT NOT NULL UNIQUE,
	p256dh     TEXT NOT NULL,
	auth       TEXT NOT NULL,
	user_agent TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	last_at    INTEGER,                   -- NULL: never delivered
	last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX push_subscriptions_org ON push_subscriptions (org_id);
CREATE INDEX push_subscriptions_user ON push_subscriptions (user_id);
