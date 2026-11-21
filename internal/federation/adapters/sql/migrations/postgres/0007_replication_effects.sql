-- Exact selected-intent proof and a byte-ordered canonical path index.
-- +up
CREATE TABLE replication_execution_effects (
    run_id TEXT NOT NULL,
    run_incarnation TEXT NOT NULL,
    operation_key TEXT NOT NULL,
    transfer_id TEXT NOT NULL,
    transfer_incarnation TEXT NOT NULL,
    intent_fingerprint TEXT NOT NULL,
    transfer_version BIGINT NOT NULL CHECK (transfer_version > 0),
    path TEXT COLLATE "C" NOT NULL,
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL,
    effect_paths_json TEXT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (run_id, run_incarnation, operation_key),
    FOREIGN KEY (run_id, run_incarnation) REFERENCES replication_executions(run_id, incarnation) ON DELETE CASCADE
);
CREATE TABLE replication_execution_effect_paths (
    run_id TEXT NOT NULL,
    run_incarnation TEXT NOT NULL,
    operation_key TEXT NOT NULL,
    path TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (run_id, run_incarnation, operation_key, path),
    FOREIGN KEY (run_id, run_incarnation, operation_key) REFERENCES replication_execution_effects(run_id, run_incarnation, operation_key) ON DELETE CASCADE
);
CREATE INDEX replication_execution_effect_paths_scope ON replication_execution_effect_paths (run_id, run_incarnation, path, operation_key);
-- +down
DROP TABLE replication_execution_effect_paths;
DROP TABLE replication_execution_effects;
