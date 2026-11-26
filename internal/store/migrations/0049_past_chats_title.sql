-- A past chat's title and message count get their own columns, so listing
-- and counting a user's past chats doesn't read and parse each one's whole
-- conversation (data, which can be megabytes). The table is built again
-- rather than altered so that the new columns come before data: SQLite then
-- reads them without following data's overflow pages.
CREATE TABLE past_chats_new (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	owner_id   TEXT NOT NULL,
	project_id TEXT NOT NULL DEFAULT '',
	user_id    INTEGER NOT NULL,
	org_id     INTEGER,
	at         INTEGER NOT NULL,
	deleted_at INTEGER,
	title      TEXT NOT NULL DEFAULT '',
	count      INTEGER NOT NULL DEFAULT 0,
	data       TEXT NOT NULL
);
INSERT INTO past_chats_new (rowid, id, kind, owner_id, project_id, user_id, org_id, at, deleted_at, title, count, data)
	SELECT rowid, id, kind, owner_id, project_id, user_id, org_id, at, deleted_at,
		COALESCE(json_extract(data, '$.title'), ''), COALESCE(json_extract(data, '$.count'), 0), data
	FROM past_chats;
DROP TABLE past_chats;
ALTER TABLE past_chats_new RENAME TO past_chats;
CREATE INDEX past_chats_owner ON past_chats (kind, owner_id, at);
