-- Personal notification webhooks: one user's, told about every project of
-- one org (the one they made it in) for as long as they are a member. They
-- have project_id '' and user_id, org_id set; a project's webhooks have
-- those NULL.
ALTER TABLE webhooks ADD COLUMN user_id INTEGER REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE webhooks ADD COLUMN org_id INTEGER REFERENCES organizations (id) ON DELETE CASCADE;
CREATE INDEX webhooks_user ON webhooks (user_id);
CREATE INDEX webhooks_org ON webhooks (org_id);

-- Each webhook's recent deliveries (internal/notify), newest kept: the last
-- 50 per webhook (store.WebhookHistory). project and subject are the
-- names at the time, so the history reads the same after a rename or
-- deletion. status is the HTTP status of the last attempt (0: no answer).
-- error "" = it went through. Times are Unix seconds.
CREATE TABLE webhook_deliveries (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	webhook_id  TEXT NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
	at          INTEGER NOT NULL,
	event       TEXT NOT NULL,
	delivery    TEXT NOT NULL DEFAULT '', -- the notification's id (Dash-Delivery)
	project_id  TEXT NOT NULL DEFAULT '',
	project     TEXT NOT NULL DEFAULT '',
	subject     TEXT NOT NULL DEFAULT '',
	attempts    INTEGER NOT NULL DEFAULT 1,
	status      INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX webhook_deliveries_webhook ON webhook_deliveries (webhook_id, id);

-- Deliveries so far: each webhook's latest, as the history's first entry.
INSERT INTO webhook_deliveries (webhook_id, at, event, project_id, error)
	SELECT id, last_at, last_event, project_id, last_error FROM webhooks WHERE last_at IS NOT NULL;
