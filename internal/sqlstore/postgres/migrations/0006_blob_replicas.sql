-- Which mirror members hold which blobs (decision 0028), for the hot tier and
-- the archive tier. updated_at is 0 when unset rather than NULL, because the
-- repair worker pages by (updated_at, sha256, member) and the engines sort
-- NULLs differently.
-- +up
CREATE TABLE blob_replicas (
    sha256     TEXT COLLATE "C" NOT NULL,
    member     TEXT COLLATE "C" NOT NULL,
    state      TEXT NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (sha256, member)
);
CREATE INDEX blob_replicas_queue ON blob_replicas (state, member, updated_at, sha256);
CREATE INDEX blob_replicas_member ON blob_replicas (member, updated_at, sha256);
CREATE TABLE cold_blob_replicas (
    sha256     TEXT COLLATE "C" NOT NULL,
    member     TEXT COLLATE "C" NOT NULL,
    state      TEXT NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (sha256, member)
);
CREATE INDEX cold_blob_replicas_queue ON cold_blob_replicas (state, member, updated_at, sha256);
CREATE INDEX cold_blob_replicas_member ON cold_blob_replicas (member, updated_at, sha256);
-- +down
DROP TABLE cold_blob_replicas;
DROP TABLE blob_replicas;
