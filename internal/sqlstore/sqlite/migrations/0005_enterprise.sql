-- Enterprise products: release bundles and Distribution, the cold-storage
-- archive tier and Workers (research 15 section 4).
-- +up
CREATE TABLE cold_blobs (
    sha256     TEXT PRIMARY KEY,
    sha1       TEXT NOT NULL DEFAULT '',
    md5        TEXT NOT NULL DEFAULT '',
    size       BIGINT NOT NULL,
    created_at BIGINT,
    ref_count  BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX cold_blobs_sha1 ON cold_blobs (sha1);
CREATE INDEX cold_blobs_md5 ON cold_blobs (md5);
CREATE INDEX cold_blobs_unreferenced ON cold_blobs (ref_count, sha256);

CREATE TABLE release_bundles (
    name              TEXT NOT NULL,
    version           TEXT NOT NULL,
    state             TEXT NOT NULL,
    storing_repo      TEXT NOT NULL DEFAULT '',
    project_key       TEXT NOT NULL DEFAULT '',
    origin            TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    created_at        BIGINT,
    created_by        TEXT NOT NULL DEFAULT '',
    signed_at         BIGINT,
    signing_key_alias TEXT NOT NULL DEFAULT '',
    signature         BLOB,
    notes             TEXT,
    spec              TEXT,
    PRIMARY KEY (name, version)
);
CREATE INDEX release_bundles_project ON release_bundles (project_key, name, version);

-- The frozen artifact list; one entry per target path.
CREATE TABLE bundle_artifacts (
    bundle_name    TEXT NOT NULL,
    bundle_version TEXT NOT NULL,
    target_path    TEXT NOT NULL,
    source_path    TEXT NOT NULL DEFAULT '',
    sha256         TEXT NOT NULL DEFAULT '',
    sha1           TEXT NOT NULL DEFAULT '',
    md5            TEXT NOT NULL DEFAULT '',
    size           BIGINT NOT NULL DEFAULT 0,
    props          TEXT,
    PRIMARY KEY (bundle_name, bundle_version, target_path),
    FOREIGN KEY (bundle_name, bundle_version) REFERENCES release_bundles (name, version) ON DELETE CASCADE
);
CREATE INDEX bundle_artifacts_sha256 ON bundle_artifacts (sha256);

-- Retention keep marks; research 15's bundle_retention row split into marks
-- (removed with the version) and the audit trail (kept after it).
CREATE TABLE bundle_keeps (
    bundle_name    TEXT NOT NULL,
    bundle_version TEXT NOT NULL,
    PRIMARY KEY (bundle_name, bundle_version),
    FOREIGN KEY (bundle_name, bundle_version) REFERENCES release_bundles (name, version) ON DELETE CASCADE
);

CREATE TABLE bundle_audits (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    bundle_name    TEXT NOT NULL,
    bundle_version TEXT NOT NULL,
    operation      TEXT NOT NULL,
    created_at     BIGINT,
    created_by     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX bundle_audits_bundle ON bundle_audits (bundle_name, created_at, id);
CREATE INDEX bundle_audits_created ON bundle_audits (created_at, id);

CREATE TABLE bundle_exports (
    bundle_name    TEXT NOT NULL,
    bundle_version TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT '',
    archive_sha256 TEXT NOT NULL DEFAULT '',
    created_at     BIGINT,
    PRIMARY KEY (bundle_name, bundle_version),
    FOREIGN KEY (bundle_name, bundle_version) REFERENCES release_bundles (name, version) ON DELETE CASCADE
);

CREATE TABLE distribution_trackers (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    bundle_name        TEXT NOT NULL,
    bundle_version     TEXT NOT NULL,
    kind               TEXT NOT NULL,
    dry_run            BOOLEAN NOT NULL DEFAULT FALSE,
    auto_create_repos  BOOLEAN NOT NULL DEFAULT FALSE,
    status             TEXT NOT NULL DEFAULT '',
    created_at         BIGINT,
    created_by         TEXT NOT NULL DEFAULT '',
    rules              TEXT
);
CREATE INDEX distribution_trackers_bundle ON distribution_trackers (bundle_name, bundle_version, id);

CREATE TABLE distribution_jobs (
    tracker_id  BIGINT NOT NULL REFERENCES distribution_trackers (id) ON DELETE CASCADE,
    peer        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0,
    started_at  BIGINT,
    finished_at BIGINT,
    error       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tracker_id, peer)
);
CREATE INDEX distribution_jobs_peer ON distribution_jobs (peer, tracker_id);

-- Both JFrog policy families (retention v1, smart archiving v2).
CREATE TABLE archive_policies (
    key               TEXT PRIMARY KEY,
    family            TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    enabled           BOOLEAN NOT NULL DEFAULT FALSE,
    cron_exp          TEXT NOT NULL DEFAULT '',
    project_key       TEXT NOT NULL DEFAULT '',
    duration_minutes  INTEGER NOT NULL DEFAULT 0,
    expiration_months INTEGER NOT NULL DEFAULT 0,
    skip_trashcan     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        BIGINT,
    updated_at        BIGINT,
    criteria          TEXT
);
CREATE INDEX archive_policies_family ON archive_policies (family, key);

-- Archive, restore and purge executions.
CREATE TABLE archive_executions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_key    TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL,
    state         TEXT NOT NULL,
    started_at    BIGINT,
    finished_at   BIGINT,
    total         BIGINT NOT NULL DEFAULT 0,
    completed     BIGINT NOT NULL DEFAULT 0,
    failed        BIGINT NOT NULL DEFAULT 0,
    total_bytes   BIGINT NOT NULL DEFAULT 0,
    success_bytes BIGINT NOT NULL DEFAULT 0,
    failure_bytes BIGINT NOT NULL DEFAULT 0,
    triggered_by  TEXT NOT NULL DEFAULT '',
    destination   TEXT
);
CREATE INDEX archive_executions_policy ON archive_executions (policy_key, id);
CREATE INDEX archive_executions_kind ON archive_executions (kind, id);

-- Archived items keep the AQL items-domain columns for retention search and
-- the full item, properties and statistics as documents for restore.
CREATE TABLE archived_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace        TEXT NOT NULL DEFAULT '',
    repo_key         TEXT NOT NULL,
    path             TEXT NOT NULL,
    name             TEXT NOT NULL,
    depth            INTEGER NOT NULL DEFAULT 0,
    sha256           TEXT NOT NULL DEFAULT '',
    sha1             TEXT NOT NULL DEFAULT '',
    md5              TEXT NOT NULL DEFAULT '',
    size             BIGINT NOT NULL DEFAULT 0,
    created_at       BIGINT,
    created_by       TEXT NOT NULL DEFAULT '',
    modified_at      BIGINT,
    modified_by      TEXT NOT NULL DEFAULT '',
    package_type     TEXT NOT NULL DEFAULT '',
    package_name     TEXT NOT NULL DEFAULT '',
    package_version  TEXT NOT NULL DEFAULT '',
    policy_key       TEXT NOT NULL DEFAULT '',
    execution_id     BIGINT NOT NULL DEFAULT 0,
    archived_at      BIGINT,
    expires_at       BIGINT,
    cold_blob_sha256 TEXT NOT NULL DEFAULT '',
    item             TEXT NOT NULL,
    properties       TEXT,
    stats            TEXT
);
CREATE INDEX archived_items_location ON archived_items (repo_key, path, name, id);
CREATE INDEX archived_items_policy ON archived_items (policy_key, id);
CREATE INDEX archived_items_execution ON archived_items (execution_id, id);
CREATE INDEX archived_items_expires ON archived_items (expires_at, id);
CREATE INDEX archived_items_sha256 ON archived_items (sha256);
CREATE INDEX archived_items_sha1 ON archived_items (sha1);

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

CREATE TABLE worker_state (
    worker_key TEXT NOT NULL REFERENCES workers (key) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (worker_key, key)
);

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
-- +down
DROP TABLE worker_executions;
DROP TABLE worker_secrets;
DROP TABLE worker_state;
DROP TABLE workers;
DROP TABLE archived_items;
DROP TABLE archive_executions;
DROP TABLE archive_policies;
DROP TABLE distribution_jobs;
DROP TABLE distribution_trackers;
DROP TABLE bundle_exports;
DROP TABLE bundle_audits;
DROP TABLE bundle_keeps;
DROP TABLE bundle_artifacts;
DROP TABLE release_bundles;
DROP TABLE cold_blobs;
