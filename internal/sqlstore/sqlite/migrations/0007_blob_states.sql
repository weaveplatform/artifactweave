-- Where each blob stands in garbage collection (decision 0010). A row is
-- marked deleting before its content is removed and becomes deleted (a
-- tombstone) afterwards, so a deploy of the same content never references
-- content being removed. deleted_at is kept on a revived row until pruned:
-- a deploy that finds it set checks the content is still there.
-- +up
ALTER TABLE blobs ADD COLUMN state TEXT NOT NULL DEFAULT 'live';
ALTER TABLE blobs ADD COLUMN deleted_at BIGINT;
CREATE INDEX blobs_state ON blobs (state, sha256);
ALTER TABLE cold_blobs ADD COLUMN state TEXT NOT NULL DEFAULT 'live';
ALTER TABLE cold_blobs ADD COLUMN deleted_at BIGINT;
CREATE INDEX cold_blobs_state ON cold_blobs (state, sha256);
-- +down
DROP INDEX cold_blobs_state;
ALTER TABLE cold_blobs DROP COLUMN deleted_at;
ALTER TABLE cold_blobs DROP COLUMN state;
DROP INDEX blobs_state;
ALTER TABLE blobs DROP COLUMN deleted_at;
ALTER TABLE blobs DROP COLUMN state;
