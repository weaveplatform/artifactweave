-- Sensitive repository HTTP header values live outside the public document.
-- The application backfills version-zero rows with its configured keyring before
-- opening the store; DDL alone cannot encrypt a value using the operator's key.
-- +up
ALTER TABLE repositories ADD COLUMN header_secrets_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE repository_header_secrets (
    repo_key TEXT NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    value BLOB,
    PRIMARY KEY (repo_key, position)
);
-- +down
DROP TABLE repository_header_secrets;
ALTER TABLE repositories DROP COLUMN header_secrets_version;
