-- Profile pictures (Account → Profile picture). users.avatar_at is the
-- picture's version, Unix milliseconds (0: none): its URL carries it, so a
-- new picture isn't hidden by a cached one. The image itself is kept apart,
-- so reading users doesn't read it.
ALTER TABLE users ADD COLUMN avatar_at INTEGER NOT NULL DEFAULT 0;

CREATE TABLE user_avatars (
	user_id      INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
	content_type TEXT NOT NULL,
	data         BLOB NOT NULL
);
