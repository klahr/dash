-- A GitHub webhook per organization and repository, not per repository: each
-- org registers its own, with its own secret and URL, through its own
-- runners, so one org's runner never gets another org's secret. The saved
-- webhooks are dropped, not shared out: the hook manager registers them
-- again, updating the webhook already on GitHub (its URL and a new secret).
DROP TABLE IF EXISTS github_hooks;
CREATE TABLE github_hooks (
	org_id        INTEGER NOT NULL DEFAULT 0,
	repo          TEXT NOT NULL,
	hook_id       INTEGER NOT NULL DEFAULT 0,
	secret        TEXT NOT NULL DEFAULT '',
	url           TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	registered    INTEGER NOT NULL DEFAULT 0,
	last_delivery INTEGER NOT NULL DEFAULT 0,
	last_ping     INTEGER NOT NULL DEFAULT 0,
	checked       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (org_id, repo)
);
