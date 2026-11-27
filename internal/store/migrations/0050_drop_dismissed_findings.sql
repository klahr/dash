-- Deleting a draft no longer dismisses its issues: a scan or Claude may
-- report them again. The dismissed issues kept so far are dropped.
DROP TABLE IF EXISTS dismissed_findings;
