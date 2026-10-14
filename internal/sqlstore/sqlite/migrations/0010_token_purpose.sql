-- Purposed tokens (decision 0005): the reference token that stands in for a
-- user's encrypted password keeps its value, sealed, so a repeat request
-- shows the same one; the unique index keeps one per user and purpose.
-- +up
ALTER TABLE tokens ADD COLUMN purpose TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN secret BLOB;
CREATE UNIQUE INDEX tokens_purpose ON tokens (subject, purpose) WHERE purpose <> '';
-- +down
DROP INDEX tokens_purpose;
ALTER TABLE tokens DROP COLUMN secret;
ALTER TABLE tokens DROP COLUMN purpose;
