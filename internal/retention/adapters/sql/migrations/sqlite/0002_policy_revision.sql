-- +up
ALTER TABLE cleanup_policies ADD COLUMN revision TEXT NOT NULL DEFAULT '';

-- +down
ALTER TABLE cleanup_policies DROP COLUMN revision;
