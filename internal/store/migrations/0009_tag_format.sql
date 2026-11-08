-- Release tags (History → Tag release…) are named in a per-project format:
-- X, Y and Z stand for major, minor and patch, e.g. "vX.Y.Z". Projects are
-- stored as JSON; existing ones get the default, "X.Y.Z", written out so the
-- setting shows what is used. A repository whose release workflow runs on
-- other tags (the Dash repository's on "v*") is pointed at it in settings.
UPDATE projects SET data = json_set(data, '$.tag_format', 'X.Y.Z')
	WHERE json_extract(data, '$.tag_format') IS NULL;
