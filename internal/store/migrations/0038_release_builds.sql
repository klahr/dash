-- Release builds: each release tag of a project and how the GitHub Actions
-- runs it started went (model.ReleaseBuild as JSON in data, its state and
-- push time beside it), so following a release survives a restart.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS release_builds (
	project_id TEXT NOT NULL,
	tag        TEXT NOT NULL,
	org_id     INTEGER,
	state      TEXT NOT NULL,
	pushed     INTEGER NOT NULL,
	data       TEXT NOT NULL,
	PRIMARY KEY (project_id, tag)
);
CREATE INDEX IF NOT EXISTS release_builds_pushed ON release_builds (project_id, pushed);
