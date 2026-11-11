-- The workers stream: Workers with their sealed secrets, executions and
-- state (research 15 section 4).
-- +up

CREATE TABLE workers (
    key           TEXT PRIMARY KEY,
    action        TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT FALSE,
    debug         BOOLEAN NOT NULL DEFAULT FALSE,
    shared        BOOLEAN NOT NULL DEFAULT FALSE,
    project_key   TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT '',
    program_hash  TEXT NOT NULL DEFAULT '',
    failures      INTEGER NOT NULL DEFAULT 0,
    blocked_until BIGINT,
    created_at    BIGINT,
    updated_at    BIGINT,
    filter_doc    TEXT
);
CREATE INDEX workers_action ON workers (action, key);

CREATE TABLE worker_secrets (
    worker_key TEXT NOT NULL REFERENCES workers (key) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    value      BLOB,
    PRIMARY KEY (worker_key, name)
);

CREATE TABLE worker_executions (
    id           TEXT PRIMARY KEY,
    worker_key   TEXT NOT NULL REFERENCES workers (key) ON DELETE CASCADE,
    action       TEXT NOT NULL DEFAULT '',
    trigger_kind TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',
    started_at   BIGINT NOT NULL,
    duration_ms  BIGINT NOT NULL DEFAULT 0,
    payload      BLOB,
    result       BLOB,
    logs         BLOB,
    error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX worker_executions_started ON worker_executions (started_at, id);
CREATE INDEX worker_executions_worker ON worker_executions (worker_key, started_at, id);

CREATE TABLE worker_state (
    worker_key TEXT NOT NULL REFERENCES workers (key) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (worker_key, key)
);
-- +down
DROP TABLE worker_state;
DROP TABLE worker_executions;
DROP TABLE worker_secrets;
DROP TABLE workers;
