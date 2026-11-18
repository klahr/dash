-- A planning card's assignee: the member of the org who owns it (NULL:
-- nobody). An account that is deleted leaves its cards unassigned.
ALTER TABLE planning_cards ADD COLUMN assignee INTEGER REFERENCES users (id) ON DELETE SET NULL;
