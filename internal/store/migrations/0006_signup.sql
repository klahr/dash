-- Open signup (off by default): accounts made from the signup form wait for
-- their email address to be confirmed before they can sign in, and every
-- such account gets a personal workspace, an org with only them in it.

-- unverified: signed up, the email's link not clicked yet. Accounts made
-- any other way (the env account, invitations) are 0.
ALTER TABLE users ADD COLUMN unverified INTEGER NOT NULL DEFAULT 0;

-- personal_user_id: the org is that user's personal workspace. It never has
-- other members (it can't invite), and each user has at most one.
ALTER TABLE organizations ADD COLUMN personal_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX organizations_personal ON organizations (personal_user_id) WHERE personal_user_id IS NOT NULL;

-- Single-use links sent by email, by SHA-256 of the token: confirming an
-- address after signing up (verify), and choosing a new password (reset).
CREATE TABLE email_tokens (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	purpose    TEXT NOT NULL CHECK (purpose IN ('verify', 'reset')),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX email_tokens_user ON email_tokens (user_id, purpose);
