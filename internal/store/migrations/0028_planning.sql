-- Planning: the org's plan for a product owner, a board of cards in fixed
-- columns (Ideas, Planned, In progress, Done). A card may name one of the
-- org's projects, and links to the tasks and goals that carry it out; its
-- status is worked out from those, never stored. Times are Unix seconds.
--
-- The planner role: a member who uses only Planning. SQLite can't change a
-- CHECK constraint in place, so the two tables that name the roles are
-- copied into new ones that allow it (nothing references either table).

CREATE TABLE org_memberships_new (
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	role       TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'planner')),
	created_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, org_id)
);
INSERT INTO org_memberships_new (user_id, org_id, role, created_at)
	SELECT user_id, org_id, role, created_at FROM org_memberships;
DROP TABLE org_memberships;
ALTER TABLE org_memberships_new RENAME TO org_memberships;
CREATE INDEX org_memberships_org ON org_memberships (org_id);

CREATE TABLE org_invitations_new (
	id         INTEGER PRIMARY KEY,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	email      TEXT NOT NULL COLLATE NOCASE,
	role       TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'planner')),
	token_hash TEXT NOT NULL UNIQUE,
	invited_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	any_email  INTEGER NOT NULL DEFAULT 1,
	UNIQUE (org_id, email)
);
INSERT INTO org_invitations_new (id, org_id, email, role, token_hash, invited_by, created_at, expires_at, any_email)
	SELECT id, org_id, email, role, token_hash, invited_by, created_at, expires_at, any_email FROM org_invitations;
DROP TABLE org_invitations;
ALTER TABLE org_invitations_new RENAME TO org_invitations;

-- project_id is '' for a card of no project in particular. position orders
-- the cards of a column, from 0.
CREATE TABLE planning_cards (
	id          TEXT PRIMARY KEY,
	org_id      INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	project_id  TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	col         TEXT NOT NULL CHECK (col IN ('ideas', 'planned', 'in_progress', 'done')),
	position    INTEGER NOT NULL,
	priority    TEXT NOT NULL DEFAULT '' CHECK (priority IN ('', 'high', 'medium', 'low')),
	target      TEXT NOT NULL DEFAULT '',
	created_by  INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE INDEX planning_cards_org ON planning_cards (org_id, col, position);

CREATE TABLE planning_comments (
	id         INTEGER PRIMARY KEY,
	card_id    TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	author     INTEGER REFERENCES users (id) ON DELETE SET NULL,
	text       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX planning_comments_card ON planning_comments (card_id);

-- The tasks (kind 'task', a todo's id) and goals ('goal') that carry a card
-- out. A link outlives its task or goal: the card then shows it as removed.
CREATE TABLE planning_links (
	card_id    TEXT NOT NULL REFERENCES planning_cards (id) ON DELETE CASCADE,
	kind       TEXT NOT NULL CHECK (kind IN ('task', 'goal')),
	work_id    TEXT NOT NULL,
	created_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (card_id, kind, work_id)
);
CREATE INDEX planning_links_work ON planning_links (kind, work_id);
