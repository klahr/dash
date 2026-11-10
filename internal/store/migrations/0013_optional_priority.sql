-- Task priority is optional: an unset priority is null (absent) and sorts
-- after every prioritised task, oldest first. Before, 0 meant both "unset"
-- and "first", so existing 0s become unset. Other values are kept.
UPDATE todos SET data = json_set(data, '$.priority', json('null'))
	WHERE json_extract(data, '$.priority') = 0;
