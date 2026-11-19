-- +up
CREATE TABLE schedules (
    kind       TEXT NOT NULL,
    key        TEXT NOT NULL,
    revision   TEXT NOT NULL,
    expression TEXT NOT NULL,
    timezone   TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL,
    next_fire  BIGINT,
    PRIMARY KEY (kind, key)
);
CREATE INDEX schedules_due ON schedules(enabled, next_fire, kind, key);

-- +down
DROP TABLE schedules;
