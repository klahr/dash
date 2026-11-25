-- Conversations put aside with New chat (a user's general chat or their
-- chat about a project), which they can open again. kind and owner_id are
-- as in chat_messages; project_id is '' for a general chat. data is the
-- model.PastChat with its messages. at is when it was put aside, deleted_at
-- when it was deleted (kept a while for Undo), both Unix milliseconds.
CREATE TABLE IF NOT EXISTS past_chats (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	owner_id   TEXT NOT NULL,
	project_id TEXT NOT NULL DEFAULT '',
	user_id    INTEGER NOT NULL,
	org_id     INTEGER,
	at         INTEGER NOT NULL,
	deleted_at INTEGER,
	data       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS past_chats_owner ON past_chats (kind, owner_id, at);
