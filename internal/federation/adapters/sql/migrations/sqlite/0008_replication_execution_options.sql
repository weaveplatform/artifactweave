-- Immutable per-run options share the exact execution lifetime. Passwords use
-- the registered sealed column, independently of the private JSON document.
-- +up
CREATE TABLE replication_execution_options (
    run_id TEXT NOT NULL,
    run_incarnation TEXT NOT NULL,
    options_json TEXT NOT NULL,
    password BLOB,
    PRIMARY KEY (run_id, run_incarnation),
    FOREIGN KEY (run_id, run_incarnation) REFERENCES replication_executions (run_id, incarnation) ON DELETE CASCADE
);
-- +down
DROP TABLE replication_execution_options;
