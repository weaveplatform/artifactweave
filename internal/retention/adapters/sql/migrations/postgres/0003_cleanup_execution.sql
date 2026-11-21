-- +up
ALTER TABLE cleanup_policies ADD COLUMN execution_credential JSONB;

CREATE TABLE cleanup_executions (
    run_id     BIGINT PRIMARY KEY REFERENCES cleanup_runs(id),
    occurrence TEXT UNIQUE,
    snapshot   JSONB NOT NULL
);

-- +down
DROP TABLE cleanup_executions;
ALTER TABLE cleanup_policies DROP COLUMN execution_credential;
