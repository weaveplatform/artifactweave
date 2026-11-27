-- Download evidence must remain stable across our own propagated removals,
-- while every later download invalidates an earlier cleanup observation.
-- +up
ALTER TABLE package_versions ADD COLUMN download_revision TEXT COLLATE "C" NOT NULL DEFAULT 'initial' CHECK (download_revision <> '');
ALTER TABLE package_versions ADD COLUMN admitted_at BIGINT;
ALTER TABLE package_versions ADD COLUMN admission_revision TEXT COLLATE "C" NOT NULL DEFAULT 'initial';
CREATE TABLE artifact_cleanup_state (
 repo_key TEXT COLLATE "C" PRIMARY KEY REFERENCES repositories(key) ON DELETE CASCADE,
 barrier TEXT NOT NULL DEFAULT '{}',
 admitted_at BIGINT,
 admission_revision TEXT COLLATE "C" NOT NULL DEFAULT 'initial'
);
CREATE TABLE artifact_cleanup_admissions (
 id TEXT COLLATE "C" PRIMARY KEY,
 repo_key TEXT COLLATE "C" NOT NULL REFERENCES repositories(key) ON DELETE CASCADE,
 incarnation TEXT COLLATE "C" NOT NULL,
 item_id BIGINT NOT NULL,
 repository_wide BOOLEAN NOT NULL
);
CREATE INDEX artifact_cleanup_admissions_repo_item ON artifact_cleanup_admissions(repo_key, item_id);
CREATE TABLE artifact_cleanup_admission_versions (
 admission_id TEXT COLLATE "C" NOT NULL REFERENCES artifact_cleanup_admissions(id) ON DELETE CASCADE,
 version_id BIGINT NOT NULL,
 PRIMARY KEY (admission_id, version_id)
);
CREATE INDEX artifact_cleanup_admission_versions_version ON artifact_cleanup_admission_versions(version_id, admission_id);
CREATE TABLE artifact_cleanup_reservations (
 owner TEXT COLLATE "C" NOT NULL,
 id TEXT COLLATE "C" NOT NULL,
 binding TEXT NOT NULL,
 closed BOOLEAN NOT NULL,
 PRIMARY KEY (owner, id)
);
-- +down
DROP TABLE artifact_cleanup_reservations;
DROP TABLE artifact_cleanup_admission_versions;
DROP TABLE artifact_cleanup_admissions;
DROP TABLE artifact_cleanup_state;
ALTER TABLE package_versions DROP COLUMN admission_revision;
ALTER TABLE package_versions DROP COLUMN admitted_at;
ALTER TABLE package_versions DROP COLUMN download_revision;
