-- Planning boards: an org has several boards of planning cards (one per
-- product or team, say), in the order they show. One is the org's default
-- (is_default), which can't be deleted: the cards of a board that is
-- deleted move to it, and the org's cards of before are on it. A card's
-- position orders the cards of its column on its board.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS planning_boards (
	id         TEXT PRIMARY KEY,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	position   INTEGER NOT NULL,
	is_default INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS planning_boards_org ON planning_boards (org_id, position);
CREATE UNIQUE INDEX IF NOT EXISTS planning_boards_default ON planning_boards (org_id) WHERE is_default = 1;

-- The board each card is on (a table beside planning_cards, so that this
-- migration can run again).
CREATE TABLE IF NOT EXISTS planning_card_boards (
	card_id  TEXT PRIMARY KEY REFERENCES planning_cards (id) ON DELETE CASCADE,
	board_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS planning_card_boards_board ON planning_card_boards (board_id);

-- Each org that has cards gets its default board now (others get theirs
-- when Planning is first used), and its cards go on it.
INSERT OR IGNORE INTO planning_boards (id, org_id, name, position, is_default, created_at)
	SELECT lower(hex(randomblob(6))), org_id, 'Main', 0, 1, CAST(strftime('%s', 'now') AS INTEGER)
	FROM (SELECT DISTINCT org_id FROM planning_cards);
INSERT OR IGNORE INTO planning_card_boards (card_id, board_id)
	SELECT c.id, b.id FROM planning_cards c JOIN planning_boards b ON b.org_id = c.org_id AND b.is_default = 1;
