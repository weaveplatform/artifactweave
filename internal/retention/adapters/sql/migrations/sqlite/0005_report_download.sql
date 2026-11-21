-- +up
ALTER TABLE cleanup_runs ADD COLUMN report_downloaded BOOLEAN NOT NULL DEFAULT FALSE;

-- +down
ALTER TABLE cleanup_runs DROP COLUMN report_downloaded;
