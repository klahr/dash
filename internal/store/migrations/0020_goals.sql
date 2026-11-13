-- Goals: a long-lived aim in one project that a coordinator (Claude, run
-- with the chat machinery) plans and dispatches tasks for. Stored like the
-- other records: the JSON of model.Goal, with its project and org beside it.
-- The coordinator's conversation is in chat_messages, kind 'goal'.
CREATE TABLE goals (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	org_id     INTEGER,
	data       TEXT NOT NULL
);
CREATE INDEX goals_project ON goals (project_id);
CREATE INDEX goals_org ON goals (org_id);
