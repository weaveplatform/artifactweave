-- The releases stream: release bundles with their frozen artifact lists,
-- keep marks, audit trails and exports, and Distribution's trackers and jobs
-- (research 15 section 4).
-- +up

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
    sha512         TEXT NOT NULL DEFAULT '',
    sha1           TEXT NOT NULL DEFAULT '',
    md5            TEXT NOT NULL DEFAULT '',
    size           BIGINT NOT NULL DEFAULT 0,
    props          TEXT,
    PRIMARY KEY (bundle_name, bundle_version, target_path),
    FOREIGN KEY (bundle_name, bundle_version) REFERENCES release_bundles (name, version) ON DELETE CASCADE
);
CREATE INDEX bundle_artifacts_sha256 ON bundle_artifacts (sha256);

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

-- Retention keep marks; research 15's bundle_retention row split into marks
-- (removed with the version) and the audit trail (kept after it).
CREATE TABLE bundle_keeps (
    bundle_name    TEXT NOT NULL,
    bundle_version TEXT NOT NULL,
    PRIMARY KEY (bundle_name, bundle_version),
    FOREIGN KEY (bundle_name, bundle_version) REFERENCES release_bundles (name, version) ON DELETE CASCADE
);

CREATE TABLE distribution_trackers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    bundle_name       TEXT NOT NULL,
    bundle_version    TEXT NOT NULL,
    kind              TEXT NOT NULL,
    dry_run           BOOLEAN NOT NULL DEFAULT FALSE,
    auto_create_repos BOOLEAN NOT NULL DEFAULT FALSE,
    status            TEXT NOT NULL DEFAULT '',
    created_at        BIGINT,
    created_by        TEXT NOT NULL DEFAULT '',
    rules             TEXT
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
-- +down
DROP TABLE distribution_jobs;
DROP TABLE distribution_trackers;
DROP TABLE bundle_keeps;
DROP TABLE bundle_exports;
DROP TABLE bundle_audits;
DROP TABLE bundle_artifacts;
DROP TABLE release_bundles;
