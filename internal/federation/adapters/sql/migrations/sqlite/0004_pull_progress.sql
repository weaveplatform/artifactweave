-- Private replication evidence outlives queue rows and removed target settings.
-- +up
CREATE TABLE replication_pull_runs (
    run_id          TEXT PRIMARY KEY,
    incarnation     TEXT NOT NULL,
    target_key      TEXT NOT NULL,
    binding_json    TEXT NOT NULL,
    version         BIGINT NOT NULL CHECK (version > 0),
    phase           TEXT NOT NULL,
    source_cursor   TEXT NOT NULL DEFAULT '',
    target_cursor   TEXT NOT NULL DEFAULT '',
    source_done     BOOLEAN NOT NULL DEFAULT FALSE,
    target_done     BOOLEAN NOT NULL DEFAULT FALSE,
    started_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    completed_at    BIGINT,
    UNIQUE (run_id, incarnation)
);
CREATE INDEX replication_pull_runs_target ON replication_pull_runs (target_key, phase, run_id);

CREATE TABLE replication_pull_items (
    run_id              TEXT NOT NULL,
    run_incarnation     TEXT NOT NULL,
    operation_key       TEXT NOT NULL,
    intent_fingerprint  TEXT NOT NULL,
    action              TEXT NOT NULL,
    path                TEXT NOT NULL,
    version             BIGINT NOT NULL CHECK (version > 0),
    phase               TEXT NOT NULL,
    document            TEXT NOT NULL,
    PRIMARY KEY (run_id, operation_key),
    FOREIGN KEY (run_id, run_incarnation) REFERENCES replication_pull_runs (run_id, incarnation) ON DELETE CASCADE
);
CREATE INDEX replication_pull_items_pending ON replication_pull_items (run_id, phase, operation_key);
-- +down
DROP TABLE replication_pull_items;
DROP TABLE replication_pull_runs;
