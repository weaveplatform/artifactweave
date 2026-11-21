-- No authenticated author can be reconstructed from a legacy target. NULL
-- preserves that fact; only a later authenticated write can activate scheduling.
-- +up
ALTER TABLE replications ADD COLUMN schedule_author_json TEXT;
-- +down
ALTER TABLE replications DROP COLUMN schedule_author_json;
