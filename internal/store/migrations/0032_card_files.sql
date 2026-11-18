-- The files attached to a planning card: uploads (upload_id names the
-- upload's directory), with their name, type and size, in the order they
-- were added. The upload itself stays on disk; cleanup keeps it while a
-- card has it.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS planning_card_files (
	card_id   TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	upload_id TEXT NOT NULL,
	name      TEXT NOT NULL,
	type      TEXT NOT NULL,
	size      INTEGER NOT NULL,
	added_by  INTEGER REFERENCES users (id) ON DELETE SET NULL,
	added_at  INTEGER NOT NULL,
	PRIMARY KEY (card_id, upload_id)
);
