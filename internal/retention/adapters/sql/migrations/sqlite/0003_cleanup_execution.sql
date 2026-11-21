-- +up
ALTER TABLE cleanup_policies ADD COLUMN execution_credential TEXT;
ALTER TABLE cleanup_policies ADD COLUMN project_incarnation TEXT NOT NULL DEFAULT '';

CREATE TABLE cleanup_executions (
    run_id     BIGINT PRIMARY KEY REFERENCES cleanup_runs(id),
    occurrence TEXT UNIQUE,
    snapshot   TEXT NOT NULL
);

-- +down
DROP TABLE cleanup_executions;
ALTER TABLE cleanup_policies DROP COLUMN project_incarnation;
ALTER TABLE cleanup_policies DROP COLUMN execution_credential;
