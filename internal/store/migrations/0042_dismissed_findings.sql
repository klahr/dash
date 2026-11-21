-- Issues become draft tasks on the Board. The issues a user dismissed are
-- kept here, by fingerprint, so reports of them add no draft. The issues
-- table itself is turned into tasks and dropped by Go code when the store
-- opens (store.migrateIssues): its prompts are written from the issues.
CREATE TABLE IF NOT EXISTS dismissed_findings (
	project_id  TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	org_id      INTEGER,
	data        TEXT NOT NULL,
	PRIMARY KEY (project_id, fingerprint)
);
