-- Plans: a new project being planned with Claude (an idea, the interview
-- and the brief it writes), before it is launched as a project and a goal.
-- Each is one user's, in one org: the JSON of model.Plan, with its owner
-- and org beside it. They go with their user or org (ON DELETE CASCADE),
-- and a member's plans in an org go when they leave it (RemoveMember).
-- Times are Unix seconds.
CREATE TABLE plans (
	id         TEXT PRIMARY KEY,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	data       TEXT NOT NULL,
	launched   INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX plans_owner ON plans (org_id, user_id);
