-- The projects each user starred in each org they are in: the sidebar
-- lists them first and folds the rest away. project_key names a project
-- as the browser sees it: its id for a project of this Dash, or
-- "<runner>/<id>" for one only a runner has (the runner keeps its own
-- projects, with their own ids). created_at is Unix seconds.
CREATE TABLE IF NOT EXISTS starred_projects (
	user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id      INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	project_key TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (user_id, org_id, project_key)
);
CREATE INDEX IF NOT EXISTS starred_projects_key ON starred_projects (org_id, project_key);
