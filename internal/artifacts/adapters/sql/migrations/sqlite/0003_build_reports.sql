-- What a keyed build delete or promotion reported (#1410, decision 0043,
-- "Replay for keyed operations"): the replay's receipt names a row here, and
-- a retry answers with its report rather than run the action again. No
-- foreign key to builds: a delete's runs are gone by the time it reports.
-- Each keyed action prunes expired rows before it adds one, through the
-- expiry index.
-- +up

CREATE TABLE build_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    action      TEXT NOT NULL,
    project_key TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL,
    build_id    BIGINT NOT NULL DEFAULT 0,
    report      TEXT NOT NULL,
    created_at  BIGINT NOT NULL,
    expires_at  BIGINT NOT NULL
);
CREATE INDEX build_reports_expiry ON build_reports (expires_at, id);

-- +down
DROP INDEX build_reports_expiry;
DROP TABLE build_reports;
