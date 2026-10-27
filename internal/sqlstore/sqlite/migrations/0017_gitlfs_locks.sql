-- +up
CREATE TABLE gitlfs_locks (
    id TEXT PRIMARY KEY,
    repo_key TEXT NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path TEXT NOT NULL,
    owner TEXT NOT NULL,
    locked_at BIGINT NOT NULL,
    UNIQUE (repo_key, path)
);
CREATE INDEX gitlfs_locks_repo_path ON gitlfs_locks (repo_key, path);
-- +down
DROP TABLE gitlfs_locks;
