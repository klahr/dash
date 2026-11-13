-- Goals: a long-lived aim in one project that a coordinator (Claude, run
-- with the chat machinery) plans and dispatches tasks for. Stored like the
-- other records: the JSON of model.Goal, with its project and org beside it.
-- The coordinator's conversation is in chat_messages, kind 'goal'.
--
-- This was briefly numbered 0020, alongside 0020_claude_status.sql, so a
-- database at version 20 may have either table and not the other. Every
-- statement here is IF NOT EXISTS, and claude_status (as in 0020) is created
-- too, so both kinds of database end up with the same schema.
CREATE TABLE IF NOT EXISTS goals (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	org_id     INTEGER,
	data       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS goals_project ON goals (project_id);
CREATE INDEX IF NOT EXISTS goals_org ON goals (org_id);

CREATE TABLE IF NOT EXISTS claude_status (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	data TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
