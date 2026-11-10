-- Signing in with Google (OpenID Connect; internal/auth/google.go). Dash is
-- the relying party: Google proves who someone is, Dash decides which orgs
-- they may enter. Times are Unix seconds.

-- has_password: 0 for accounts made by signing in with Google, whose hash
-- is of a random secret no one knows. They can choose one with a password
-- reset link. The env account and every older account have one.
ALTER TABLE users ADD COLUMN has_password INTEGER NOT NULL DEFAULT 1;

-- How a session was signed in ('password' or 'google') and when that
-- happened (auth_at). A session handed to an org's subdomain keeps both
-- from the sign-in it came from.
ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT 'password';
ALTER TABLE sessions ADD COLUMN auth_at INTEGER NOT NULL DEFAULT 0;

-- any_email: anyone with the link may accept, whatever their address (how
-- every invitation worked before). With it off, accepting by signing in
-- with Google needs a Google account with the invited address.
ALTER TABLE org_invitations ADD COLUMN any_email INTEGER NOT NULL DEFAULT 1;

-- An identity at an OpenID provider linked to an account. Sign-ins match on
-- (provider, subject), the provider's stable id for the person, never on
-- the email address, which can change hands. email and hd are what the
-- provider said at the last sign-in.
CREATE TABLE user_identities (
	user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	provider      TEXT NOT NULL,
	subject       TEXT NOT NULL,
	email         TEXT NOT NULL DEFAULT '',
	hd            TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	last_login_at INTEGER,
	UNIQUE (provider, subject),
	UNIQUE (user_id, provider)
);

-- An org's single sign-on settings. google_client_secret is sealed with
-- the hub's secret key (auth.Sealer), never stored in the clear.
CREATE TABLE org_sso (
	org_id               INTEGER PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
	google_client_id     TEXT NOT NULL DEFAULT '',
	google_client_secret TEXT NOT NULL DEFAULT '',
	auto_join            INTEGER NOT NULL DEFAULT 0,
	join_role            TEXT NOT NULL DEFAULT 'member' CHECK (join_role IN ('admin', 'member')),
	require_google       INTEGER NOT NULL DEFAULT 0,
	google_2fa           INTEGER NOT NULL DEFAULT 0,
	updated_at           INTEGER NOT NULL
);

-- Google Workspace domains an org claims. A claim counts once verified: by
-- an owner signing in with Google with that hosted domain, or by a platform
-- admin. A verified domain belongs to one org only; unverified claims don't
-- block anyone.
CREATE TABLE org_sso_domains (
	org_id      INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	domain      TEXT NOT NULL COLLATE NOCASE,
	verified    INTEGER NOT NULL DEFAULT 0,
	how         TEXT NOT NULL DEFAULT '',
	verified_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at  INTEGER NOT NULL,
	verified_at INTEGER,
	PRIMARY KEY (org_id, domain)
);
CREATE UNIQUE INDEX org_sso_domains_verified ON org_sso_domains (domain) WHERE verified = 1;

-- The instance's own Google client, when a platform admin sets it in
-- settings rather than DASH_GOOGLE_CLIENT_ID / _SECRET. Sealed like org_sso's.
CREATE TABLE instance_sso (
	id                   INTEGER PRIMARY KEY CHECK (id = 1),
	google_client_id     TEXT NOT NULL DEFAULT '',
	google_client_secret TEXT NOT NULL DEFAULT '',
	updated_at           INTEGER NOT NULL
);

-- What happened with sign-ins and their settings: Google sign-ins and
-- failures, links and unlinks, SSO setting changes, members signed out.
CREATE TABLE auth_events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	at       INTEGER NOT NULL,
	org_id   INTEGER,
	user_id  INTEGER,
	actor_id INTEGER,
	kind     TEXT NOT NULL,
	detail   TEXT NOT NULL DEFAULT '',
	ip       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX auth_events_org ON auth_events (org_id, id);
