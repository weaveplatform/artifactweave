-- +up
-- No zero backfill: an existing project's first guarded admission initializes
-- from its current owned artifact metadata in the same publication transaction.
CREATE TABLE project_storage (
    project_key TEXT PRIMARY KEY REFERENCES projects(key) ON DELETE CASCADE,
    bytes       INTEGER NOT NULL CHECK (bytes >= 0),
    warning_key TEXT NOT NULL DEFAULT ''
);

-- +down
DROP TABLE project_storage;
