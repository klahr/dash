-- A platform admin's billing override (see docs/billing.md#free-organizations):
-- the org needs no subscription while free_forever is set, or until
-- free_until (Unix seconds; 0 = no date). free_note is the admin's reason,
-- free_set_by the admin's username and free_set_at when they set it.
ALTER TABLE org_billing ADD COLUMN free_forever INTEGER NOT NULL DEFAULT 0;
ALTER TABLE org_billing ADD COLUMN free_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE org_billing ADD COLUMN free_note TEXT NOT NULL DEFAULT '';
ALTER TABLE org_billing ADD COLUMN free_set_by TEXT NOT NULL DEFAULT '';
ALTER TABLE org_billing ADD COLUMN free_set_at INTEGER NOT NULL DEFAULT 0;
