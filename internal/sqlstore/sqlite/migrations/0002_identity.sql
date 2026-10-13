-- Identity: users, groups and memberships, permissions with their grant and
-- target mirrors, access tokens, and key material (key pairs, CA
-- certificates, trusted release-bundle keys, Distribution signing keys).
-- Research 15 section 4. The groups table is user_groups because GROUPS is a
-- window-frame keyword in SQLite.
-- +up
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
    roles                      TEXT
);
CREATE TABLE user_groups (
    name             TEXT PRIMARY KEY,
    description      TEXT NOT NULL DEFAULT '',
    auto_join        BOOLEAN NOT NULL DEFAULT FALSE,
    admin_privileges BOOLEAN NOT NULL DEFAULT FALSE,
    realm            TEXT NOT NULL DEFAULT '',
    realm_attributes TEXT NOT NULL DEFAULT '',
    external_id      TEXT NOT NULL DEFAULT '',
    roles            TEXT
);
CREATE INDEX user_groups_auto_join ON user_groups (auto_join, name);
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
    last_used_at     BIGINT
);
CREATE INDEX tokens_subject ON tokens (subject, issued_at, id);
CREATE INDEX tokens_issued ON tokens (issued_at, id);
CREATE INDEX tokens_expires ON tokens (expires_at);
CREATE TABLE keypairs (
    name        TEXT PRIMARY KEY,
    type        TEXT NOT NULL DEFAULT '',
    alias       TEXT NOT NULL DEFAULT '',
    public_key  TEXT NOT NULL DEFAULT '',
    private_key BLOB,
    passphrase  BLOB
);
CREATE TABLE certificates (
    alias       TEXT PRIMARY KEY,
    pem         TEXT NOT NULL DEFAULT '',
    issued_to   TEXT NOT NULL DEFAULT '',
    issued_by   TEXT NOT NULL DEFAULT '',
    issued_on   BIGINT,
    valid_until BIGINT,
    fingerprint TEXT NOT NULL DEFAULT ''
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
-- +down
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
