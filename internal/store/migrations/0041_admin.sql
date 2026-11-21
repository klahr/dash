-- Platform admins manage every account (the Admin sheet). disabled_at: when
-- a platform admin disabled the account (NULL: it signs in); a disabled
-- account has no sessions and its tokens don't work. last_login_at: its
-- latest sign-in (NULL: none since this was recorded). An account without a
-- row is enabled and hasn't signed in since.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS user_status (
	user_id       INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
	disabled_at   INTEGER,
	last_login_at INTEGER
);
