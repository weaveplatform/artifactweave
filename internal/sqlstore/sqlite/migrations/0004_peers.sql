-- Peers: this instance's identity, paired peers, pairing nonces, the
-- outbox with its sequences, peer cursors, applied markers and failed blob
-- fetches (decision 0021); federated repository members and full syncs
-- (decision 0022); Access Federation targets, platform federations and
-- topologies (decision 0025). Research 15 section 4.
-- +up
CREATE TABLE instance (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    instance_id      TEXT NOT NULL,
    public_key       BLOB,
    private_key      BLOB,
    namespace_id     TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE peers (
    instance_id      TEXT PRIMARY KEY,
    base_url         TEXT NOT NULL DEFAULT '',
    public_key       BLOB,
    protocol_version INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT '',
    last_seen_at     BIGINT,
    display_name     TEXT NOT NULL DEFAULT '',
    city_name        TEXT NOT NULL DEFAULT '',
    country_code     TEXT NOT NULL DEFAULT '',
    edge_role        BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE peer_use_cases (
    peer     TEXT NOT NULL REFERENCES peers (instance_id) ON DELETE CASCADE,
    use_case TEXT NOT NULL,
    PRIMARY KEY (peer, use_case)
);
CREATE INDEX peer_use_cases_use_case ON peer_use_cases (use_case, peer);
CREATE TABLE pairing_nonces (
    nonce_hash  BLOB PRIMARY KEY,
    use_case    TEXT NOT NULL,
    expires_at  BIGINT,
    redeemed_at BIGINT
);
CREATE INDEX pairing_nonces_expires ON pairing_nonces (expires_at);
CREATE TABLE outbox_sequences (
    origin   TEXT NOT NULL,
    stream   TEXT NOT NULL,
    last_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (origin, stream)
);
CREATE TABLE outbox (
    origin      TEXT NOT NULL,
    stream      TEXT NOT NULL,
    seq         BIGINT NOT NULL,
    hlc_wall    BIGINT NOT NULL,
    hlc_logical BIGINT NOT NULL,
    event_type  TEXT NOT NULL DEFAULT '',
    payload     BLOB,
    blob_sha256 TEXT NOT NULL DEFAULT '',
    blob_size   BIGINT NOT NULL DEFAULT 0,
    created_at  BIGINT,
    PRIMARY KEY (origin, stream, seq)
);
CREATE TABLE peer_cursors (
    peer         TEXT NOT NULL,
    stream       TEXT NOT NULL,
    acked_seq    BIGINT NOT NULL DEFAULT 0,
    last_sent_at BIGINT,
    last_error   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (peer, stream)
);
CREATE TABLE applied_markers (
    origin   TEXT NOT NULL,
    stream   TEXT NOT NULL,
    last_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (origin, stream)
);
CREATE TABLE failed_blob_fetches (
    stream          TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    sha1            TEXT NOT NULL DEFAULT '',
    repo_key        TEXT NOT NULL DEFAULT '',
    first_failed_at BIGINT,
    failures        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (stream, sha256)
);
CREATE TABLE federation_members (
    repo_key        TEXT NOT NULL,
    peer            TEXT NOT NULL,
    base_url        TEXT NOT NULL DEFAULT '',
    remote_repo_key TEXT NOT NULL DEFAULT '',
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (repo_key, peer)
);
CREATE INDEX federation_members_peer ON federation_members (peer, repo_key);
CREATE TABLE full_sync_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_key     TEXT NOT NULL,
    peer         TEXT NOT NULL,
    forced       BOOLEAN NOT NULL DEFAULT FALSE,
    snapshot_seq BIGINT NOT NULL DEFAULT 0,
    started_at   BIGINT,
    finished_at  BIGINT,
    status       TEXT NOT NULL DEFAULT '',
    result_paths TEXT
);
CREATE INDEX full_sync_runs_repo ON full_sync_runs (repo_key, id);
CREATE TABLE access_federation_targets (
    name               TEXT PRIMARY KEY,
    peer               TEXT NOT NULL,
    active             BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         BIGINT,
    entities           TEXT,
    permission_filters TEXT,
    group_filters      TEXT
);
CREATE TABLE platform_federations (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    connection_groups TEXT,
    entities          TEXT
);
CREATE TABLE topologies (
    key         TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at  BIGINT,
    modified_at BIGINT
);
CREATE TABLE topology_sites (
    topology_key TEXT NOT NULL REFERENCES topologies (key) ON DELETE CASCADE,
    site_name    TEXT NOT NULL,
    peer         TEXT NOT NULL,
    PRIMARY KEY (topology_key, site_name)
);
-- +down
DROP TABLE topology_sites;
DROP TABLE topologies;
DROP TABLE platform_federations;
DROP TABLE access_federation_targets;
DROP TABLE full_sync_runs;
DROP TABLE federation_members;
DROP TABLE failed_blob_fetches;
DROP TABLE applied_markers;
DROP TABLE peer_cursors;
DROP TABLE outbox;
DROP TABLE outbox_sequences;
DROP TABLE pairing_nonces;
DROP TABLE peer_use_cases;
DROP TABLE peers;
DROP TABLE instance;
