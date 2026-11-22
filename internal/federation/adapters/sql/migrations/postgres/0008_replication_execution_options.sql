-- Immutable per-run options share the exact execution lifetime. Passwords use
-- the registered sealed column, independently of the private JSON document.
-- +up
CREATE TABLE replication_execution_options (
    run_id TEXT NOT NULL,
    run_incarnation TEXT NOT NULL,
    options_json TEXT NOT NULL,
    independent BOOLEAN NOT NULL DEFAULT FALSE,
    target_url TEXT NOT NULL,
    password BYTEA,
    PRIMARY KEY (run_id, run_incarnation),
    FOREIGN KEY (run_id, run_incarnation) REFERENCES replication_executions (run_id, incarnation) ON DELETE CASCADE
);
CREATE INDEX replication_execution_options_target ON replication_execution_options (independent, run_id, run_incarnation);
CREATE INDEX replication_executions_destination ON replication_executions (repo_key, repo_incarnation, target_key, run_id, incarnation);
-- +down
DROP INDEX replication_executions_destination;
DROP TABLE replication_execution_options;
