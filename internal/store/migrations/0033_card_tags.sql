-- A planning card's tags: short labels, any number, in the order they were
-- added. Each once per card, whatever its case.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS planning_card_tags (
	card_id  TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	tag      TEXT NOT NULL COLLATE NOCASE,
	position INTEGER NOT NULL,
	PRIMARY KEY (card_id, tag)
);
CREATE INDEX IF NOT EXISTS planning_card_tags_tag ON planning_card_tags (tag);
