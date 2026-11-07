-- Org subdomains (off unless a base domain is configured): each org is
-- served at <slug>.<base domain>, so its slug is now a DNS label its owners
-- choose and may rename.

-- folder: the org's folder under the projects root. It used to be the slug;
-- now that a slug can change (and an old one be claimed by another org
-- later), the folder is fixed when the org is made and never changes, so no
-- org ever gets another's folder.
ALTER TABLE organizations ADD COLUMN folder TEXT;
UPDATE organizations SET folder = slug;
CREATE UNIQUE INDEX organizations_folder ON organizations (folder COLLATE NOCASE);

-- A renamed org's old slug keeps redirecting to it, and can't be claimed by
-- another org, until expires_at.
CREATE TABLE org_slug_redirects (
	slug       TEXT PRIMARY KEY COLLATE NOCASE,
	org_id     INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE INDEX org_slug_redirects_org ON org_slug_redirects (org_id);

-- A browser's sessions on the base domain and on the org subdomains it was
-- handed to share a family, so signing out on one host signs out them all
-- (the cookies are host-only, so one host can't clear another's).
ALTER TABLE sessions ADD COLUMN family TEXT;
CREATE INDEX sessions_family ON sessions (family) WHERE family IS NOT NULL;
