-- Task events reach the task's creator by default, not the whole org:
-- users who saved their notification settings before get "tasks": "mine"
-- (Account → Notifications → Tasks), and the summaries' activity keeps
-- who created each task (0: Dash, or an account that is gone).
UPDATE notify_settings SET settings = json_set(settings, '$.tasks', 'mine')
	WHERE json_extract(settings, '$.tasks') IS NULL;
ALTER TABLE notify_activity ADD COLUMN creator INTEGER NOT NULL DEFAULT 0;
