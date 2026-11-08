-- Outgoing notification webhooks (model.Webhook), per project: Dash posts
-- task, approval and build events to them (internal/notify). secret signs
-- generic JSON deliveries and token is an optional bearer token (ntfy);
-- both are needed in the clear to send, and never leave Dash except in the
-- delivery itself (the secret once more, when it's made or rotated). The
-- last_* columns describe the latest delivery. Times are Unix seconds.
CREATE TABLE webhooks (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '',
	url        TEXT NOT NULL,
	format     TEXT NOT NULL,
	events     TEXT NOT NULL,             -- JSON array of event names
	enabled    INTEGER NOT NULL DEFAULT 1,
	secret     TEXT NOT NULL,
	token      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	last_at    INTEGER,                   -- NULL: never delivered
	last_event TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX webhooks_project ON webhooks (project_id);
