-- An org's settings for its planners. no_project_context: a planner's
-- feature brief interview gets only the card and the project's name, not
-- the project's CLAUDE.md, README and file tree (they get them by default).
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS org_planner_settings (
	org_id             INTEGER PRIMARY KEY,
	no_project_context INTEGER NOT NULL DEFAULT 0
);
