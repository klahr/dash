-- A domain claim is verified by a DNS TXT record on the domain that holds
-- its token (or by a platform admin). An owner's Google sign-in with that
-- hosted domain no longer counts: it shows the owner has an account in the
-- Workspace, not that they run the domain.
ALTER TABLE org_sso_domains ADD COLUMN token TEXT NOT NULL DEFAULT '';
UPDATE org_sso_domains SET token = lower(hex(randomblob(16))) WHERE token = '';
