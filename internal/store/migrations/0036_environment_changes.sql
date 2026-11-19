-- Environment settings history: each change to one of a project's
-- environments (model.EnvChange as JSON in data), with who made it and when.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS environment_changes (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	org_id     INTEGER,
	user_id    INTEGER NOT NULL DEFAULT 0,
	at         INTEGER NOT NULL,
	data       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS environment_changes_project ON environment_changes (project_id, id);
