-- Incident response (dash lockdown, Admin → Incident response).
--
-- What a lockdown asks of an account:
-- new_password: its next sign-in only lets it choose a new password; until
-- then its sessions and tokens are refused.
-- totp_reenroll: its two-factor sign-in was reset; it is asked to set it up
-- again at its next sign-in, as an org that requires it would.
CREATE TABLE IF NOT EXISTS user_lockdown (
	user_id       INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
	new_password  INTEGER NOT NULL DEFAULT 0,
	totp_reenroll INTEGER NOT NULL DEFAULT 0
);

-- A notice from the platform admins, shown as a banner after sign-in until
-- each user dismisses it or an admin clears it. One is current at a time:
-- the latest not cleared.
CREATE TABLE IF NOT EXISTS notices (
	id         INTEGER PRIMARY KEY,
	body       TEXT NOT NULL,
	created_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	cleared_at INTEGER
);

CREATE TABLE IF NOT EXISTS notice_dismissals (
	notice_id INTEGER NOT NULL REFERENCES notices (id) ON DELETE CASCADE,
	user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	at        INTEGER NOT NULL,
	PRIMARY KEY (notice_id, user_id)
);
