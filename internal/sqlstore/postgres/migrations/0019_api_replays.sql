-- +up
CREATE TABLE api_replays (
    scope TEXT COLLATE "C" NOT NULL,
    key_hash TEXT COLLATE "C" NOT NULL,
    fingerprint TEXT NOT NULL,
    resource_id BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    PRIMARY KEY (scope, key_hash)
);
CREATE INDEX api_replays_expiry ON api_replays (expires_at, scope, key_hash);
-- +down
DROP TABLE api_replays;
