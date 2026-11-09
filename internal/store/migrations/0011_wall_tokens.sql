-- Wall (kiosk) tokens: a read-only link to one org's wall view (/wall), for
-- a TV that never signs in. They grant nothing else. Only the hash of the
-- secret is kept. Times are Unix seconds.
CREATE TABLE wall_tokens (
	id           INTEGER PRIMARY KEY,
	org_id       INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	created_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	token_prefix TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER                  -- NULL: never used
);
CREATE INDEX wall_tokens_org ON wall_tokens (org_id);
