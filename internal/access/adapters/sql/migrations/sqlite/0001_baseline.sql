-- The access stream: users, groups and memberships; permissions with their
-- grant and target mirrors; access tokens; key material (key pairs, CA
-- certificates, trusted release-bundle keys, Distribution signing keys);
-- projects with their roles, environments, members and repository
-- assignments; API restrictions with their audit trail. Research 15
-- section 4.
-- +up

-- failed_logins, password_changed_at and password_expired serve account
-- lockout and password expiry (decision 0005): consecutive failed logins for
-- the user lock policy, and when the password was last set and whether an
-- admin expired it for the password expiration policy.
--
-- search_name is the name lowercased by Go rather than the database, so list
-- filtering does not depend on the engine's locale (decision 0043). The
-- access module fills it in, through the partial index, for any row that
-- lacks it when the store opens.
CREATE TABLE users (
    name                       TEXT PRIMARY KEY,
    email                      TEXT NOT NULL DEFAULT '',
    password_hash              BLOB,
    admin                      BOOLEAN NOT NULL DEFAULT FALSE,
    profile_updatable          BOOLEAN NOT NULL DEFAULT FALSE,
    internal_password_disabled BOOLEAN NOT NULL DEFAULT FALSE,
    disable_ui_access          BOOLEAN NOT NULL DEFAULT FALSE,
    realm                      TEXT NOT NULL DEFAULT '',
    status                     TEXT NOT NULL DEFAULT '',
    mfa                        TEXT NOT NULL DEFAULT '',
    last_logged_in_at          BIGINT,
    created_at                 BIGINT,
    roles                      TEXT,
    failed_logins              INTEGER NOT NULL DEFAULT 0,
    password_changed_at        BIGINT,
    password_expired           BOOLEAN NOT NULL DEFAULT FALSE,
    search_name                TEXT
);
CREATE INDEX users_search_backfill ON users (name) WHERE search_name IS NULL;

-- The table is user_groups because GROUPS is a window-frame keyword in
-- SQLite. search_name is filled like the one of users.
CREATE TABLE user_groups (
    name             TEXT PRIMARY KEY,
    description      TEXT NOT NULL DEFAULT '',
    auto_join        BOOLEAN NOT NULL DEFAULT FALSE,
    admin_privileges BOOLEAN NOT NULL DEFAULT FALSE,
    realm            TEXT NOT NULL DEFAULT '',
    realm_attributes TEXT NOT NULL DEFAULT '',
    external_id      TEXT NOT NULL DEFAULT '',
    roles            TEXT,
    search_name      TEXT
);
CREATE INDEX user_groups_auto_join ON user_groups (auto_join, name);
CREATE INDEX groups_search_backfill ON user_groups (name) WHERE search_name IS NULL;

CREATE TABLE group_members (
    group_name TEXT NOT NULL REFERENCES user_groups (name) ON DELETE CASCADE,
    user_name  TEXT NOT NULL REFERENCES users (name) ON DELETE CASCADE,
    PRIMARY KEY (group_name, user_name)
);
CREATE INDEX group_members_user ON group_members (user_name, group_name);

CREATE TABLE permissions (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    created_at  BIGINT,
    updated_at  BIGINT,
    resources   TEXT
);

CREATE TABLE permission_grants (
    permission_name TEXT NOT NULL REFERENCES permissions (name) ON DELETE CASCADE,
    resource_type   TEXT NOT NULL,
    principal_type  TEXT NOT NULL,
    principal_name  TEXT NOT NULL,
    actions         INTEGER NOT NULL,
    PRIMARY KEY (permission_name, resource_type, principal_type, principal_name)
);
CREATE INDEX permission_grants_principal ON permission_grants (principal_type, principal_name);

CREATE TABLE permission_targets (
    permission_name  TEXT NOT NULL REFERENCES permissions (name) ON DELETE CASCADE,
    resource_type    TEXT NOT NULL,
    target           TEXT NOT NULL,
    include_patterns TEXT,
    exclude_patterns TEXT,
    PRIMARY KEY (permission_name, resource_type, target)
);
CREATE INDEX permission_targets_target ON permission_targets (target);

-- purpose and secret serve purposed tokens (decision 0005): the reference
-- token that stands in for a user's encrypted password keeps its value,
-- sealed, so a repeat request shows the same one, and tokens_purpose keeps
-- one per user and purpose. identity is how the subject resolves to a user
-- (TokenIdentity): an OIDC token exchange may issue a token for a user who
-- is not stored (research 21).
CREATE TABLE tokens (
    id               TEXT PRIMARY KEY,
    subject          TEXT NOT NULL DEFAULT '',
    access_digest    BLOB UNIQUE,
    refresh_digest   BLOB UNIQUE,
    reference_digest BLOB UNIQUE,
    scope            TEXT NOT NULL DEFAULT '',
    audience         TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    project_key      TEXT NOT NULL DEFAULT '',
    issued_at        BIGINT NOT NULL DEFAULT 0,
    expires_at       BIGINT,
    refreshable      BOOLEAN NOT NULL DEFAULT FALSE,
    revocable        BOOLEAN NOT NULL DEFAULT FALSE,
    last_used_at     BIGINT,
    purpose          TEXT NOT NULL DEFAULT '',
    secret           BLOB,
    identity         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX tokens_subject ON tokens (subject, issued_at, id);
CREATE INDEX tokens_issued ON tokens (issued_at, id);
CREATE INDEX tokens_expires ON tokens (expires_at);
CREATE UNIQUE INDEX tokens_purpose ON tokens (subject, purpose) WHERE purpose <> '';

CREATE TABLE keypairs (
    name        TEXT PRIMARY KEY,
    type        TEXT NOT NULL DEFAULT '',
    alias       TEXT NOT NULL DEFAULT '',
    public_key  TEXT NOT NULL DEFAULT '',
    private_key BLOB,
    passphrase  BLOB
);

-- private_key is a client certificate's private key, kept apart from the
-- certificate's PEM and sealed like every other private key (decision
-- 0017).
CREATE TABLE certificates (
    alias       TEXT PRIMARY KEY,
    pem         TEXT NOT NULL DEFAULT '',
    issued_to   TEXT NOT NULL DEFAULT '',
    issued_by   TEXT NOT NULL DEFAULT '',
    issued_on   BIGINT,
    valid_until BIGINT,
    fingerprint TEXT NOT NULL DEFAULT '',
    private_key BLOB
);

CREATE TABLE trusted_keys (
    kid         TEXT PRIMARY KEY,
    alias       TEXT UNIQUE,
    public_key  TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    issued_on   BIGINT,
    issued_by   TEXT NOT NULL DEFAULT '',
    valid_until BIGINT
);

CREATE TABLE signing_keys (
    alias       TEXT PRIMARY KEY,
    public_key  TEXT NOT NULL DEFAULT '',
    private_key BLOB,
    passphrase  BLOB,
    is_default  BOOLEAN NOT NULL DEFAULT FALSE
);

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

CREATE TABLE api_restriction_audit (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_name  TEXT NOT NULL,
    actor      TEXT NOT NULL,
    reason     TEXT NOT NULL,
    until_at   BIGINT,
    changed_at BIGINT NOT NULL
);
CREATE INDEX api_restriction_audit_user ON api_restriction_audit (user_name, id DESC);

CREATE TABLE api_restrictions (
    user_name  TEXT PRIMARY KEY REFERENCES users (name) ON DELETE CASCADE,
    id         BIGINT NOT NULL,
    actor      TEXT NOT NULL,
    reason     TEXT NOT NULL,
    until_at   BIGINT,
    changed_at BIGINT NOT NULL
);
-- +down
DROP TABLE api_restrictions;
DROP TABLE api_restriction_audit;
DROP TABLE project_repository_shares;
DROP TABLE project_repositories;
DROP TABLE project_members;
DROP TABLE environments;
DROP TABLE roles;
DROP TABLE projects;
DROP TABLE signing_keys;
DROP TABLE trusted_keys;
DROP TABLE certificates;
DROP TABLE keypairs;
DROP TABLE tokens;
DROP TABLE permission_targets;
DROP TABLE permission_grants;
DROP TABLE permissions;
DROP TABLE group_members;
DROP TABLE user_groups;
DROP TABLE users;
