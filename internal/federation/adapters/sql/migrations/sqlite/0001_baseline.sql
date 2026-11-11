-- The federation stream: replications and their runs; paired peers, their
-- use cases and pairing nonces (decision 0021); federated repository members
-- and full syncs (decision 0022); Access Federation targets, platform
-- federations and topologies (decision 0025). Research 15 section 4.
-- +up

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
DROP TABLE pairing_nonces;
DROP TABLE peer_use_cases;
DROP TABLE peers;
DROP TABLE replication_runs;
DROP TABLE replications;
