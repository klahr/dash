-- Domains verified by an owner's Google sign-in (how = 'google') were
-- verified on proof that no longer counts (see 0019): the owner had an
-- account in the Workspace, not control of the domain. They go back to
-- unverified, and must pass the DNS TXT check or a platform admin's
-- approval to count again.
UPDATE org_sso_domains SET verified = 0, how = '', verified_by = NULL, verified_at = NULL WHERE how = 'google';
