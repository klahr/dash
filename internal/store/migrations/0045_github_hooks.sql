-- The webhooks a hub registered on GitHub repositories, so GitHub tells it
-- of workflow runs (internal/ci, hooks.go): one per repository (OWNER/REPO
-- in lower case), shared by the projects that use it. secret signs the
-- deliveries and is never sent to a browser. Times are Unix milliseconds,
-- 0 for never.
CREATE TABLE IF NOT EXISTS github_hooks (
	repo          TEXT PRIMARY KEY,
	hook_id       INTEGER NOT NULL DEFAULT 0,
	secret        TEXT NOT NULL DEFAULT '',
	url           TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	registered    INTEGER NOT NULL DEFAULT 0,
	last_delivery INTEGER NOT NULL DEFAULT 0,
	last_ping     INTEGER NOT NULL DEFAULT 0,
	checked       INTEGER NOT NULL DEFAULT 0
);
