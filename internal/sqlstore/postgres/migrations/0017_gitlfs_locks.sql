-- +up
CREATE TABLE gitlfs_locks (
    id TEXT COLLATE "C" PRIMARY KEY,
    repo_key TEXT COLLATE "C" NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path TEXT COLLATE "C" NOT NULL,
    owner TEXT NOT NULL,
    locked_at BIGINT NOT NULL,
    UNIQUE (repo_key, path)
);
CREATE INDEX gitlfs_locks_repo_path ON gitlfs_locks (repo_key, path);
-- +down
DROP TABLE gitlfs_locks;
