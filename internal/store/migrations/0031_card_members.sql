-- A planning card's members: the people in the org on it, any number, in
-- the order they were added. They replace its one assignee: each card's
-- assignee becomes its member. planning_cards.assignee stays, unused
-- (SQLite can't drop a column that names another table).
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS planning_card_members (
	card_id  TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	added_at INTEGER NOT NULL,
	PRIMARY KEY (card_id, user_id)
);
CREATE INDEX IF NOT EXISTS planning_card_members_user ON planning_card_members (user_id);
INSERT OR IGNORE INTO planning_card_members (card_id, user_id, added_at)
	SELECT id, assignee, updated_at FROM planning_cards WHERE assignee IS NOT NULL;
UPDATE planning_cards SET assignee = NULL;
