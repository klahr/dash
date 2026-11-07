-- Dash's first schema: the records of state.json, one row each. A record is
-- stored as the JSON of its model type (data); the columns beside it are
-- what lookups and deletes need. No foreign keys: deletes are explicit in
-- the store, and old state.json files may hold dangling references.

CREATE TABLE projects (
	id   TEXT PRIMARY KEY,
	data TEXT NOT NULL
);

CREATE TABLE todos (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	data       TEXT NOT NULL
);
CREATE INDEX todos_project ON todos (project_id);

CREATE TABLE issues (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	data       TEXT NOT NULL
);
CREATE INDEX issues_project ON issues (project_id);

CREATE TABLE scan_runs (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	scan       TEXT NOT NULL,
	data       TEXT NOT NULL
);
CREATE INDEX scan_runs_project ON scan_runs (project_id, scan);

CREATE TABLE scan_settings (
	project_id TEXT NOT NULL,
	scan       TEXT NOT NULL,
	data       TEXT NOT NULL,
	PRIMARY KEY (project_id, scan)
);

-- Conversations: kind is 'main' (owner_id ''), 'todo' or 'project'; seq
-- orders the messages of one conversation from 0.
CREATE TABLE chat_messages (
	kind     TEXT NOT NULL,
	owner_id TEXT NOT NULL,
	seq      INTEGER NOT NULL,
	data     TEXT NOT NULL,
	PRIMARY KEY (kind, owner_id, seq)
);
