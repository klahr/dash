-- Users, organizations and who belongs where. Unlike the record tables these
-- are plain relational rows with foreign keys (the connection enables them).
-- Times are Unix seconds, except password_changed_at: nanoseconds, so a token
-- issued just before a password change is told apart from one just after.

-- is_platform_admin is about the Dash instance itself and is separate from
-- any org role. env_managed marks the account DASH_USERNAME / DASH_PASSWORD
-- describe: Dash creates it on first start and keeps its name and password in
-- step with the environment.
CREATE TABLE users (
	id                  INTEGER PRIMARY KEY,
	username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
	email               TEXT UNIQUE COLLATE NOCASE,
	password_hash       TEXT NOT NULL,
	is_platform_admin   INTEGER NOT NULL DEFAULT 0,
	env_managed         INTEGER NOT NULL DEFAULT 0,
	password_changed_at INTEGER NOT NULL,
	created_at          INTEGER NOT NULL
);
CREATE UNIQUE INDEX users_env_managed ON users (env_managed) WHERE env_managed = 1;

CREATE TABLE organizations (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	slug       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	created_at INTEGER NOT NULL
);

CREATE TABLE org_memberships (
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	role       TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
	created_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, org_id)
);
CREATE INDEX org_memberships_org ON org_memberships (org_id);

-- An invitation waits here until the invitee accepts it (it becomes a
-- membership) or declines it, or it expires. It is addressed to an email but
-- redeemed with its token: the link is the proof, so inviting never has to
-- look up, or reveal, whether the address already has an account.
CREATE TABLE org_invitations (
	id         INTEGER PRIMARY KEY,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	email      TEXT NOT NULL COLLATE NOCASE,
	role       TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
	token_hash TEXT NOT NULL UNIQUE,
	invited_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	UNIQUE (org_id, email)
);

-- Browser sessions, by the hash of the cookie. active_org_id is the org the
-- user is working in; membership is checked again on every request.
CREATE TABLE sessions (
	key           TEXT PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	active_org_id INTEGER REFERENCES organizations (id) ON DELETE SET NULL,
	created_at    INTEGER NOT NULL,
	expires_at    INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);

-- One row: the org records belong to until they are scoped by org.
CREATE TABLE instance (
	id             INTEGER PRIMARY KEY CHECK (id = 1),
	default_org_id INTEGER REFERENCES organizations (id) ON DELETE SET NULL
);
INSERT INTO instance (id) VALUES (1);

-- Every record belongs to an org. Rows are written with the default org and
-- keep the one they have on update; Accounts.Bootstrap assigns the rows that
-- existed before there were orgs.
ALTER TABLE projects ADD COLUMN org_id INTEGER;
ALTER TABLE todos ADD COLUMN org_id INTEGER;
ALTER TABLE issues ADD COLUMN org_id INTEGER;
ALTER TABLE scan_runs ADD COLUMN org_id INTEGER;
ALTER TABLE scan_settings ADD COLUMN org_id INTEGER;
ALTER TABLE scan_templates ADD COLUMN org_id INTEGER;
ALTER TABLE chat_messages ADD COLUMN org_id INTEGER;
CREATE INDEX projects_org ON projects (org_id);
