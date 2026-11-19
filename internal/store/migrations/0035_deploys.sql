-- Deploys: each deploy of a project to one of its environments, stored like
-- the other records (the JSON of model.Deploy, with its project, environment
-- and org beside it). Its output is in log, apart from data: it is read with
-- the one deploy, never kept in memory with the list.
-- Safe to run again (tests step user_version back and migrate once more).
CREATE TABLE IF NOT EXISTS deploys (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	environment TEXT NOT NULL,
	org_id      INTEGER,
	data        TEXT NOT NULL,
	log         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS deploys_project ON deploys (project_id, environment);
CREATE INDEX IF NOT EXISTS deploys_org ON deploys (org_id);

-- Environments: a project's single deploy command (and the runner labels it
-- ran on) becomes its environment "production". A project with
-- environments already keeps them. The old fields go either way.
UPDATE projects SET data = json_set(data, '$.environments', json_array(json_object(
		'name', 'production',
		'deploy_command', json_extract(data, '$.deploy_command'),
		'runs_on', json(COALESCE(json_extract(data, '$.deploy_runs_on'), '[]')))))
	WHERE COALESCE(json_extract(data, '$.deploy_command'), '') <> ''
	AND json_array_length(COALESCE(json_extract(data, '$.environments'), '[]')) = 0;
UPDATE projects SET data = json_remove(data, '$.deploy_command', '$.deploy_runs_on')
	WHERE json_extract(data, '$.deploy_command') IS NOT NULL OR json_extract(data, '$.deploy_runs_on') IS NOT NULL;
