-- A project has one task board: the extra boards a project could have are
-- gone. Tasks that were on one of them are on the project's board, and the
-- projects' lists of boards are dropped. Nothing else about the tasks changes.
-- Safe to run again (tests step user_version back and migrate once more).
UPDATE todos SET data = json_remove(data, '$.board')
	WHERE json_type(data, '$.board') IS NOT NULL;

UPDATE projects SET data = json_remove(data, '$.boards')
	WHERE json_type(data, '$.boards') IS NOT NULL;
