-- Dash moves a planning card to Done once all its linked work is done.
-- A card in planning_auto_done_held was moved out of Done by someone: Dash
-- leaves it be until new work is linked to it. A comment in
-- planning_dash_notes is Dash's own note on its card (why it moved it), not
-- a person's. (Tables, not columns, so the migration can run again.)
CREATE TABLE IF NOT EXISTS planning_auto_done_held (
	card_id TEXT PRIMARY KEY REFERENCES planning_cards (id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS planning_dash_notes (
	comment_id INTEGER PRIMARY KEY REFERENCES planning_comments (id) ON DELETE CASCADE
);
