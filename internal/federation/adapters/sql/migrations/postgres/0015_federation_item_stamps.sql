-- The conflict rule's record of each item of a federated repository (FED-02,
-- decisions 0021 and 0022): the hybrid logical clock time and origin of the
-- change that left the item as it is here, a tombstone when that change
-- deleted it, its content, and the payload of a winning change still waiting
-- for its blob. Tombstones go once older than the outbox retention window,
-- which the (deleted, hlc_wall) index finds; (repo_key, sha256) answers which
-- content a member may fetch and which records wait for a blob. 0014 belongs
-- to PEER-05's snapshots. Records waiting for a blob are listed by path, so
-- repo_key and path are COLLATE "C", ordering as SQLite does.
-- +up
CREATE TABLE federation_item_stamps (
    repo_key    TEXT COLLATE "C" NOT NULL,
    path        TEXT COLLATE "C" NOT NULL,
    hlc_wall    BIGINT NOT NULL,
    hlc_logical BIGINT NOT NULL,
    origin      TEXT NOT NULL,
    deleted     BOOLEAN NOT NULL DEFAULT FALSE,
    folder      BOOLEAN NOT NULL DEFAULT FALSE,
    sha256      TEXT NOT NULL DEFAULT '',
    size        BIGINT NOT NULL DEFAULT 0,
    pending     TEXT,
    PRIMARY KEY (repo_key, path)
);
CREATE INDEX federation_item_stamps_sha256 ON federation_item_stamps (repo_key, sha256);
CREATE INDEX federation_item_stamps_tombstones ON federation_item_stamps (deleted, hlc_wall);
-- +down
DROP INDEX federation_item_stamps_tombstones;
DROP INDEX federation_item_stamps_sha256;
DROP TABLE federation_item_stamps;
