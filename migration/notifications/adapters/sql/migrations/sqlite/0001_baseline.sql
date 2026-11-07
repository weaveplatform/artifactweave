-- The notifications stream: webhooks, their sealed secrets and their
-- deliveries (research 15 section 4).
-- +up

CREATE TABLE webhooks (
    key         TEXT PRIMARY KEY,
    project_key TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    debug       BOOLEAN NOT NULL DEFAULT FALSE,
    domain      TEXT NOT NULL DEFAULT '',
    created_at  BIGINT,
    updated_at  BIGINT,
    doc         TEXT
);
CREATE INDEX webhooks_domain ON webhooks (domain, key);

-- name is '' for a webhook handler's single secret and the secret's name for
-- a custom webhook's named secrets.
CREATE TABLE webhook_secrets (
    webhook_key   TEXT NOT NULL REFERENCES webhooks (key) ON DELETE CASCADE,
    handler_index INTEGER NOT NULL,
    name          TEXT NOT NULL,
    value         BLOB,
    PRIMARY KEY (webhook_key, handler_index, name)
);

CREATE TABLE webhook_deliveries (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_key  TEXT NOT NULL REFERENCES webhooks (key) ON DELETE CASCADE,
    event_id     TEXT NOT NULL DEFAULT '',
    event_type   TEXT NOT NULL DEFAULT '',
    attempted_at BIGINT,
    status       INTEGER NOT NULL DEFAULT 0,
    elapsed_ms   BIGINT NOT NULL DEFAULT 0,
    request      TEXT,
    response     TEXT
);
CREATE INDEX webhook_deliveries_webhook ON webhook_deliveries (webhook_key, attempted_at);
CREATE INDEX webhook_deliveries_attempted ON webhook_deliveries (attempted_at);
-- +down
DROP TABLE webhook_deliveries;
DROP TABLE webhook_secrets;
DROP TABLE webhooks;
