-- A planning card's dependencies: the other cards of its org that must be
-- done before it, in the order they were added. Deleting either card drops
-- the dependency.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS planning_card_deps (
	card_id    TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	depends_on TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	added_at   INTEGER NOT NULL,
	PRIMARY KEY (card_id, depends_on)
);
CREATE INDEX IF NOT EXISTS planning_card_deps_on ON planning_card_deps (depends_on);
