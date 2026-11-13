-- Claude's status (status.claude.com): a platform admin's settings for the
-- Statuspage webhook this Dash listens on, and the incidents and component
-- statuses it heard about, as one JSON document (internal/claudestatus).
CREATE TABLE claude_status (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	data TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
