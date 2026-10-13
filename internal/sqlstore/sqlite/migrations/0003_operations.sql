-- Operations: projects with their roles, environments, members and
-- repository assignments; webhooks and deliveries; cleanup policies and
-- runs; replications; settings; leases; configuration documents
-- (research 15 section 4).
-- +up
CREATE TABLE projects (
    key                 TEXT PRIMARY KEY,
    display_name        TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    storage_quota_bytes BIGINT NOT NULL DEFAULT 0,
    soft_limit          BOOLEAN NOT NULL DEFAULT FALSE,
    quota_email         BOOLEAN NOT NULL DEFAULT FALSE,
    admin_privileges    TEXT
);
-- Global roles have project_key ''.
CREATE TABLE roles (
    project_key  TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    type         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    environments TEXT,
    actions      TEXT,
    PRIMARY KEY (project_key, name)
);
-- Environment names are unique across the instance; global ones have
-- project_key ''.
CREATE TABLE environments (
    name        TEXT PRIMARY KEY,
    project_key TEXT NOT NULL DEFAULT ''
);
CREATE INDEX environments_project ON environments (project_key, name);
CREATE TABLE project_members (
    project_key    TEXT NOT NULL REFERENCES projects (key) ON DELETE CASCADE,
    principal_type TEXT NOT NULL,
    principal_name TEXT NOT NULL,
    roles          TEXT,
    PRIMARY KEY (project_key, principal_type, principal_name)
);
CREATE TABLE project_repositories (
    repo_key         TEXT PRIMARY KEY,
    project_key      TEXT NOT NULL DEFAULT '',
    environments     TEXT,
    shared_with_all  BOOLEAN NOT NULL DEFAULT FALSE,
    shared_read_only BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at       BIGINT
);
CREATE INDEX project_repositories_project ON project_repositories (project_key, repo_key);
CREATE TABLE project_repository_shares (
    repo_key    TEXT NOT NULL REFERENCES project_repositories (repo_key) ON DELETE CASCADE,
    project_key TEXT NOT NULL,
    PRIMARY KEY (repo_key, project_key)
);
CREATE INDEX project_repository_shares_project ON project_repository_shares (project_key, repo_key);

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

CREATE TABLE cleanup_policies (
    key              TEXT PRIMARY KEY,
    description      TEXT NOT NULL DEFAULT '',
    cron_exp         TEXT NOT NULL DEFAULT '',
    project_key      TEXT NOT NULL DEFAULT '',
    item_type        TEXT NOT NULL DEFAULT '',
    duration_minutes INTEGER NOT NULL DEFAULT 0,
    enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    skip_trashcan    BOOLEAN NOT NULL DEFAULT FALSE,
    criteria         TEXT,
    created_at       BIGINT,
    created_by       TEXT NOT NULL DEFAULT '',
    updated_at       BIGINT,
    updated_by       TEXT NOT NULL DEFAULT ''
);
-- Runs outlive their policy (the run views keep them), so there is no
-- foreign key to cleanup_policies.
CREATE TABLE cleanup_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_key    TEXT NOT NULL,
    trigger_id    BIGINT NOT NULL DEFAULT 0,
    item_type     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',
    dry_run       BOOLEAN NOT NULL DEFAULT FALSE,
    manual        BOOLEAN NOT NULL DEFAULT FALSE,
    triggered_by  TEXT NOT NULL DEFAULT '',
    started_at    BIGINT,
    ended_at      BIGINT,
    total_items   BIGINT NOT NULL DEFAULT 0,
    cleaned_items BIGINT NOT NULL DEFAULT 0,
    cleaned_size  BIGINT NOT NULL DEFAULT 0,
    report        TEXT
);
CREATE INDEX cleanup_runs_policy ON cleanup_runs (policy_key, id);

CREATE TABLE replications (
    replication_key        TEXT PRIMARY KEY,
    repo_key               TEXT NOT NULL,
    url                    TEXT NOT NULL DEFAULT '',
    username               TEXT NOT NULL DEFAULT '',
    password               BLOB,
    enabled                BOOLEAN NOT NULL DEFAULT FALSE,
    cron_exp               TEXT NOT NULL DEFAULT '',
    event_replication      BOOLEAN NOT NULL DEFAULT FALSE,
    sync_deletes           BOOLEAN NOT NULL DEFAULT FALSE,
    sync_properties        BOOLEAN NOT NULL DEFAULT FALSE,
    sync_statistics        BOOLEAN NOT NULL DEFAULT FALSE,
    include_prefix         TEXT NOT NULL DEFAULT '',
    exclude_prefix         TEXT NOT NULL DEFAULT '',
    proxy                  TEXT NOT NULL DEFAULT '',
    disable_proxy          BOOLEAN NOT NULL DEFAULT FALSE,
    check_binary_existence BOOLEAN NOT NULL DEFAULT FALSE,
    socket_timeout_ms      BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX replications_repo ON replications (repo_key, replication_key);
CREATE TABLE replication_runs (
    replication_key TEXT NOT NULL REFERENCES replications (replication_key) ON DELETE CASCADE,
    started_at      BIGINT NOT NULL,
    completed_at    BIGINT,
    status          TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (replication_key, started_at)
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE leases (
    name        TEXT PRIMARY KEY,
    holder      TEXT NOT NULL,
    task        TEXT NOT NULL DEFAULT '',
    acquired_at BIGINT,
    expires_at  BIGINT NOT NULL
);

-- One table for every configuration document kind (types.ConfigKind); the
-- secrets removed from a document are sealed in config_secrets.
CREATE TABLE config_documents (
    kind       TEXT NOT NULL,
    key        TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    document   TEXT,
    updated_at BIGINT,
    PRIMARY KEY (kind, key)
);
CREATE TABLE config_secrets (
    kind  TEXT NOT NULL,
    key   TEXT NOT NULL,
    name  TEXT NOT NULL,
    value BLOB,
    PRIMARY KEY (kind, key, name),
    FOREIGN KEY (kind, key) REFERENCES config_documents (kind, key) ON DELETE CASCADE
);
-- +down
DROP TABLE config_secrets;
DROP TABLE config_documents;
DROP TABLE leases;
DROP TABLE settings;
DROP TABLE replication_runs;
DROP TABLE replications;
DROP TABLE cleanup_runs;
DROP TABLE cleanup_policies;
DROP TABLE webhook_deliveries;
DROP TABLE webhook_secrets;
DROP TABLE webhooks;
DROP TABLE project_repository_shares;
DROP TABLE project_repositories;
DROP TABLE project_members;
DROP TABLE environments;
DROP TABLE roles;
DROP TABLE projects;
