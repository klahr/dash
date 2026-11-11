-- The Favro board backend is gone from Dash. Projects that used it go back
-- to Dash's own board: their Favro settings, credentials, webhook secret and
-- sync state are dropped, and so are their tasks' links to Favro cards. The
-- tasks themselves stay in Dash as they are.

-- Each such project's history notes the move, like a switch in settings.
INSERT INTO project_events (project_id, kind, text, user_id, at)
	SELECT project_id, 'board.switched',
		'Board moved from Favro to Dash (Favro support was removed): links to its cards dropped, the tasks stay in Dash',
		0, CAST(strftime('%s', 'now') AS INTEGER)
	FROM tracker_projects WHERE backend = 'favro';

UPDATE todos SET data = json_remove(data, '$.external_ref')
	WHERE json_extract(data, '$.external_ref.provider') = 'favro';

DELETE FROM tracker_links WHERE provider = 'favro'
	OR project_id IN (SELECT project_id FROM tracker_projects WHERE backend = 'favro');

DELETE FROM tracker_projects WHERE backend = 'favro';
