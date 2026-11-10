-- Board backends (internal/tracker): each project's task board is Dash's own
-- ("dash", the default, which needs no row) or an external tracker such as
-- Favro. Everything about it is per project: the backend and its settings,
-- the status ↔ column mapping, its credentials and webhook secret (stored
-- like the notification webhooks' secrets: needed in the clear to call the
-- tracker, never sent to browsers) and the sync engine's progress.
-- Times are Unix seconds, but switched_at and cursor (the tracker's own
-- clock) are nanoseconds.
CREATE TABLE tracker_projects (
	project_id     TEXT PRIMARY KEY,
	backend        TEXT NOT NULL,
	config         TEXT NOT NULL DEFAULT '{}', -- the backend's settings (JSON)
	status_map     TEXT NOT NULL DEFAULT '{}', -- Dash status → column (JSON)
	credentials    TEXT NOT NULL DEFAULT '{}', -- secrets (JSON object of strings)
	webhook_secret TEXT NOT NULL DEFAULT '',
	switched_at    INTEGER,                    -- NULL: never switched
	cursor         INTEGER NOT NULL DEFAULT 0,
	last_sync_at   INTEGER,
	last_error     TEXT NOT NULL DEFAULT '',
	updated_at     INTEGER NOT NULL
);

-- A todo linked to its project's tracker (or chosen for export to it):
-- the card's ref, and what each side looked like at the last sync, so a
-- change is told from an echo and conflicts go to the newest writer.
CREATE TABLE tracker_links (
	todo_id     TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	provider    TEXT NOT NULL,
	external_id TEXT NOT NULL DEFAULT '',  -- '' while pending export
	url         TEXT NOT NULL DEFAULT '',
	pending     INTEGER NOT NULL DEFAULT 0,
	dash_hash   TEXT NOT NULL DEFAULT '',
	ext_hash    TEXT NOT NULL DEFAULT '',
	ext_version TEXT NOT NULL DEFAULT '',
	state       TEXT NOT NULL DEFAULT '{}',
	synced_at   INTEGER
);
CREATE INDEX tracker_links_project ON tracker_links (project_id, external_id);

-- Notable changes to a project's setup (switching its board backend), newest last.
CREATE TABLE project_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	kind       TEXT NOT NULL,
	text       TEXT NOT NULL,
	user_id    INTEGER NOT NULL DEFAULT 0,
	at         INTEGER NOT NULL
);
CREATE INDEX project_events_project ON project_events (project_id, id);
