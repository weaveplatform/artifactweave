-- Logical dispatch survives queue completion and target removal.
-- +up
CREATE TABLE replication_executions (
    run_id TEXT PRIMARY KEY,
    incarnation TEXT NOT NULL,
    target_key TEXT NOT NULL,
    repo_key TEXT NOT NULL,
    repo_incarnation TEXT NOT NULL,
    direction TEXT NOT NULL,
    occurrence TEXT UNIQUE,
    binding_json TEXT NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    phase TEXT NOT NULL,
    succeeded BIGINT NOT NULL DEFAULT 0 CHECK (succeeded >= 0),
    failed BIGINT NOT NULL DEFAULT 0 CHECK (failed >= 0),
    skipped BIGINT NOT NULL DEFAULT 0 CHECK (skipped >= 0),
    pending BIGINT NOT NULL DEFAULT 0 CHECK (pending >= 0),
    started_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    completed_at BIGINT,
    UNIQUE (run_id, incarnation)
);
CREATE INDEX replication_executions_target ON replication_executions (target_key, started_at, run_id);
CREATE INDEX replication_executions_repository ON replication_executions (repo_key, repo_incarnation, started_at, run_id);
-- +down
DROP TABLE replication_executions;
