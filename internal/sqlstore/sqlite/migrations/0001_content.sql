-- Content: repositories, items, properties, statistics, blobs, uploads,
-- trash and the package index (research 15 section 4).
-- +up
CREATE TABLE blobs (
    sha256     TEXT PRIMARY KEY,
    sha1       TEXT NOT NULL DEFAULT '',
    md5        TEXT NOT NULL DEFAULT '',
    size       BIGINT NOT NULL,
    created_at BIGINT,
    ref_count  BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX blobs_sha1 ON blobs (sha1);
CREATE INDEX blobs_md5 ON blobs (md5);
CREATE INDEX blobs_unreferenced ON blobs (ref_count, sha256);

-- The document is the repository configuration without its password, which
-- lives sealed in repository_secrets so no document read can leak it.
CREATE TABLE repositories (
    key          TEXT PRIMARY KEY,
    rclass       TEXT NOT NULL,
    package_type TEXT NOT NULL DEFAULT '',
    project_key  TEXT NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL,
    doc          TEXT NOT NULL
);
CREATE INDEX repositories_class ON repositories (rclass, key);
CREATE INDEX repositories_package_type ON repositories (package_type, key);
CREATE INDEX repositories_project ON repositories (project_key, key);
CREATE TABLE repository_secrets (
    repo_key TEXT PRIMARY KEY REFERENCES repositories (key) ON DELETE CASCADE,
    password BLOB NOT NULL
);
CREATE TABLE repository_members (
    virtual_key TEXT NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    member_key  TEXT NOT NULL,
    position    INTEGER NOT NULL,
    PRIMARY KEY (virtual_key, position)
);
CREATE INDEX repository_members_member ON repository_members (member_key, virtual_key);
CREATE TABLE repository_keypairs (
    repo_key     TEXT NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    role         TEXT NOT NULL,
    keypair_name TEXT NOT NULL,
    PRIMARY KEY (repo_key, role)
);
CREATE INDEX repository_keypairs_name ON repository_keypairs (keypair_name, repo_key);

-- Every AQL items-domain field is a column. No foreign key to repositories:
-- deleting a configuration and deleting its content are separate steps.
CREATE TABLE items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_key        TEXT NOT NULL,
    cached          BOOLEAN NOT NULL,
    path            TEXT NOT NULL,
    name            TEXT NOT NULL,
    depth           INTEGER NOT NULL,
    type            TEXT NOT NULL,
    size            BIGINT NOT NULL DEFAULT 0,
    sha256          TEXT NOT NULL DEFAULT '',
    sha1            TEXT NOT NULL DEFAULT '',
    md5             TEXT NOT NULL DEFAULT '',
    original_sha256 TEXT NOT NULL DEFAULT '',
    original_sha1   TEXT NOT NULL DEFAULT '',
    original_md5    TEXT NOT NULL DEFAULT '',
    mime_type       TEXT NOT NULL DEFAULT '',
    created_at      BIGINT,
    created_by      TEXT NOT NULL DEFAULT '',
    modified_at     BIGINT,
    modified_by     TEXT NOT NULL DEFAULT '',
    updated_at      BIGINT,
    remote_url      TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX items_natural ON items (repo_key, cached, path, name);
CREATE INDEX items_sha256 ON items (sha256);
CREATE INDEX items_sha1 ON items (sha1);
CREATE INDEX items_md5 ON items (md5);
CREATE INDEX items_modified ON items (modified_at);
CREATE INDEX items_created ON items (created_at);
CREATE TABLE item_properties (
    item_id BIGINT NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (item_id, key, value)
);
CREATE INDEX item_properties_value ON item_properties (key, value);
CREATE TABLE item_stats (
    item_id                   BIGINT PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    download_count            BIGINT NOT NULL DEFAULT 0,
    last_downloaded_at        BIGINT,
    last_downloaded_by        TEXT NOT NULL DEFAULT '',
    remote_download_count     BIGINT NOT NULL DEFAULT 0,
    remote_last_downloaded_at BIGINT,
    remote_last_downloaded_by TEXT NOT NULL DEFAULT '',
    remote_origin             TEXT NOT NULL DEFAULT '',
    remote_path               TEXT NOT NULL DEFAULT ''
);

CREATE TABLE uploads (
    id         TEXT PRIMARY KEY,
    backend    TEXT NOT NULL DEFAULT '',
    repo_key   TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL DEFAULT '',
    state      BLOB,
    bytes      BIGINT NOT NULL DEFAULT 0,
    started_at BIGINT,
    updated_at BIGINT
);
CREATE INDEX uploads_updated ON uploads (updated_at, id);

CREATE TABLE trash (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    original_repo_key TEXT NOT NULL,
    original_path     TEXT NOT NULL,
    original_name     TEXT NOT NULL,
    item_json         TEXT NOT NULL,
    deleted_at        BIGINT,
    deleted_by        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX trash_original ON trash (original_repo_key, original_path, original_name, id);
CREATE INDEX trash_deleted ON trash (deleted_at, id);

CREATE TABLE packages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_key     TEXT NOT NULL,
    package_type TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    created_at   BIGINT
);
CREATE UNIQUE INDEX packages_name ON packages (repo_key, name);
CREATE TABLE package_versions (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    package_id         BIGINT NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
    version            TEXT NOT NULL,
    order_key          BIGINT NOT NULL DEFAULT 0,
    created_at         BIGINT,
    last_downloaded_at BIGINT,
    metadata           TEXT
);
CREATE UNIQUE INDEX package_versions_version ON package_versions (package_id, version);
CREATE INDEX package_versions_order ON package_versions (package_id, order_key, version);
CREATE TABLE package_version_files (
    version_id BIGINT NOT NULL REFERENCES package_versions (id) ON DELETE CASCADE,
    item_id    BIGINT NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    PRIMARY KEY (version_id, item_id)
);
CREATE INDEX package_version_files_item ON package_version_files (item_id, version_id);
-- +down
DROP TABLE package_version_files;
DROP TABLE package_versions;
DROP TABLE packages;
DROP TABLE trash;
DROP TABLE uploads;
DROP TABLE item_stats;
DROP TABLE item_properties;
DROP TABLE items;
DROP TABLE repository_keypairs;
DROP TABLE repository_members;
DROP TABLE repository_secrets;
DROP TABLE repositories;
DROP TABLE blobs;
