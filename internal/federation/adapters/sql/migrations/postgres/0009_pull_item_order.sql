-- One single-pull enumeration page records many intents at once (#1895).
-- ordinal keeps the order they were recorded in, the owner's enumeration
-- order, so they are fetched in that order rather than by operation key.
-- +up
ALTER TABLE replication_pull_items ADD COLUMN ordinal BIGINT NOT NULL DEFAULT 0;
CREATE INDEX replication_pull_items_order ON replication_pull_items (run_id, phase, ordinal);
-- +down
DROP INDEX replication_pull_items_order;
ALTER TABLE replication_pull_items DROP COLUMN ordinal;
