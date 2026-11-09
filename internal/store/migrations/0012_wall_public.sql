-- A public wall token always opens the wall in public mode: no task titles,
-- project or runner names, failure reasons, activity or costs, whatever the
-- link's options say. 0: an ordinary (office) wall token.
ALTER TABLE wall_tokens ADD COLUMN public INTEGER NOT NULL DEFAULT 0;
