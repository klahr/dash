-- Every credential that isn't a browser session, in one table: API tokens a
-- user creates, the grants OAuth clients (claude.ai connectors) get, and
-- runners' tokens. Modelled on ../zonventure's mcp_tokens.
--
-- Each row acts as user_id in org_id. The org is required: a token never
-- falls back to another of the user's orgs, and stops working (it isn't
-- moved) once the user leaves the org it is pinned to. Secrets are kept as
-- sha256 hashes; token_prefix is the start of the secret, so a list can tell
-- tokens apart without showing them. Times are Unix seconds, except
-- created_at, in nanoseconds like users.password_changed_at, which api and
-- oauth tokens are checked against.

CREATE TABLE oauth_clients (
	id            TEXT PRIMARY KEY,           -- the client_id
	name          TEXT NOT NULL,
	redirect_uris TEXT NOT NULL,              -- JSON array
	auth_method   TEXT NOT NULL,              -- none | client_secret_post | client_secret_basic
	secret_hash   TEXT NOT NULL DEFAULT '',   -- '' for public clients
	created_at    INTEGER NOT NULL
);

CREATE TABLE tokens (
	id           INTEGER PRIMARY KEY,
	kind         TEXT NOT NULL CHECK (kind IN ('api', 'oauth', 'runner')),
	user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	org_id       INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	token_prefix TEXT NOT NULL,
	scopes       TEXT NOT NULL,               -- JSON array: read, write (api, oauth); runner
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER,                     -- NULL: never used
	expires_at   INTEGER,                     -- NULL: until revoked
	-- An OAuth grant: its access token (token_hash) is replaced, with its
	-- refresh token, on each refresh. The refresh token it replaced is kept to
	-- spot a replay, which revokes the grant.
	oauth_client_id       TEXT REFERENCES oauth_clients (id) ON DELETE CASCADE,
	refresh_hash          TEXT UNIQUE,
	previous_refresh_hash TEXT,
	refresh_expires_at    INTEGER,
	-- A runner's token: the runner (in runners.json) it lets connect.
	runner_id TEXT UNIQUE
);
CREATE INDEX tokens_user ON tokens (user_id);
CREATE INDEX tokens_previous_refresh ON tokens (previous_refresh_hash);
