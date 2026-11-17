-- Each user's notification settings in each org they are in (Account →
-- Notifications): their summary and which events push to them. settings
-- is model.NotifySettings as JSON. The summary's state: next_at is when it
-- goes out next (0: off), through the end of the period the last one
-- covered (the next starts there, so a restart neither repeats nor skips
-- anything), last_sent when one last went out (nothing to report sends
-- nothing). Times are Unix seconds.
CREATE TABLE notify_settings (
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	settings   TEXT NOT NULL,
	next_at    INTEGER NOT NULL DEFAULT 0,
	through    INTEGER NOT NULL DEFAULT 0,
	last_sent  INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, org_id)
);
CREATE INDEX notify_settings_next ON notify_settings (next_at);

-- What the summaries tell about, as the notifier saw it happen: new
-- issues, tasks ready for review, failed or merged, goals waiting for
-- their owner (user_id), builds starting to fail. Kept for 35 days.
CREATE TABLE notify_activity (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	at         INTEGER NOT NULL,
	kind       TEXT NOT NULL,
	ref        TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	url        TEXT NOT NULL DEFAULT '',
	severity   TEXT NOT NULL DEFAULT '',
	user_id    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notify_activity_at ON notify_activity (at);

-- The key that signs the summary emails' unsubscribe links: made on first
-- use and kept, so links in old emails go on working.
CREATE TABLE notify_key (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	key        TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
