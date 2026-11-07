-- Two-factor sign-in with an authenticator app (TOTP).
--
-- totp_secret is set once enrolment is confirmed; totp_pending holds the
-- secret shown as a QR code until the user confirms it with a code.
-- totp_last_step is the last 30-second period a code was accepted for, so a
-- code can't be used twice.
ALTER TABLE users ADD COLUMN totp_secret TEXT;
ALTER TABLE users ADD COLUMN totp_pending TEXT;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- Single-use recovery codes, by SHA-256 (they are 80 random bits). Using one
-- deletes its row.
CREATE TABLE recovery_codes (
	user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL,
	PRIMARY KEY (user_id, code_hash)
);

-- Owners can require two-factor sign-in of everyone in their org.
ALTER TABLE organizations ADD COLUMN require_2fa INTEGER NOT NULL DEFAULT 0;
