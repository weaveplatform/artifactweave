-- The platform stream: the tables of the capabilities every module shares.
-- Settings, leases and the persistent work queue; configuration documents
-- with their sealed secrets; this instance's identity, the outbox with its
-- sequences, peer cursors, applied markers and failed blob fetches (decision
-- 0021); upload sessions; API replay records and request budgets (decision
-- 0043). Research 15 section 4.
--
-- Text columns that lists sort or page by are COLLATE "C", so PostgreSQL
-- orders them byte by byte, as SQLite and the in-memory store do.
-- +up

CREATE TABLE settings (
    key   TEXT COLLATE "C" PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE leases (
    name        TEXT COLLATE "C" PRIMARY KEY,
    holder      TEXT NOT NULL,
    task        TEXT NOT NULL DEFAULT '',
    acquired_at BIGINT,
    expires_at  BIGINT NOT NULL
);

-- The persistent work queue (decision 0033): one row per kind and key, so
-- work asked for again for a key while it waits coalesces into one run.
-- generation fences a worker whose lease ran out; dirty_since marks work
-- asked for during a run, which then runs once more.
CREATE TABLE work_items (
    kind        TEXT COLLATE "C" NOT NULL,
    key         TEXT COLLATE "C" NOT NULL,
    payload     TEXT,
    state       TEXT NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    generation  BIGINT NOT NULL DEFAULT 0,
    not_before  BIGINT NOT NULL,
    leased_by   TEXT NOT NULL DEFAULT '',
    lease_until BIGINT,
    dirty_since BIGINT,
    last_error  TEXT NOT NULL DEFAULT '',
    enqueued_at BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE INDEX work_items_pending ON work_items (state, not_before);
CREATE INDEX work_items_leased ON work_items (state, lease_until);

-- One table for every configuration document kind (ConfigKind); the
-- secrets removed from a document are sealed in config_secrets.
CREATE TABLE config_documents (
    kind       TEXT COLLATE "C" NOT NULL,
    key        TEXT COLLATE "C" NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    document   TEXT,
    updated_at BIGINT,
    PRIMARY KEY (kind, key)
);

CREATE TABLE config_secrets (
    kind  TEXT COLLATE "C" NOT NULL,
    key   TEXT COLLATE "C" NOT NULL,
    name  TEXT COLLATE "C" NOT NULL,
    value BYTEA,
    PRIMARY KEY (kind, key, name),
    FOREIGN KEY (kind, key) REFERENCES config_documents (kind, key) ON DELETE CASCADE
);

CREATE TABLE instance (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    instance_id      TEXT NOT NULL,
    public_key       BYTEA,
    private_key      BYTEA,
    namespace_id     TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE outbox_sequences (
    origin   TEXT COLLATE "C" NOT NULL,
    stream   TEXT COLLATE "C" NOT NULL,
    last_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (origin, stream)
);

CREATE TABLE outbox (
    origin      TEXT COLLATE "C" NOT NULL,
    stream      TEXT COLLATE "C" NOT NULL,
    seq         BIGINT NOT NULL,
    hlc_wall    BIGINT NOT NULL,
    hlc_logical BIGINT NOT NULL,
    event_type  TEXT NOT NULL DEFAULT '',
    payload     BYTEA,
    blob_sha256 TEXT NOT NULL DEFAULT '',
    blob_size   BIGINT NOT NULL DEFAULT 0,
    created_at  BIGINT,
    PRIMARY KEY (origin, stream, seq)
);

CREATE TABLE peer_cursors (
    peer         TEXT COLLATE "C" NOT NULL,
    stream       TEXT COLLATE "C" NOT NULL,
    acked_seq    BIGINT NOT NULL DEFAULT 0,
    last_sent_at BIGINT,
    last_error   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (peer, stream)
);

CREATE TABLE applied_markers (
    origin   TEXT COLLATE "C" NOT NULL,
    stream   TEXT COLLATE "C" NOT NULL,
    last_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (origin, stream)
);

CREATE TABLE failed_blob_fetches (
    stream          TEXT COLLATE "C" NOT NULL,
    sha256          TEXT COLLATE "C" NOT NULL,
    sha1            TEXT NOT NULL DEFAULT '',
    repo_key        TEXT NOT NULL DEFAULT '',
    first_failed_at BIGINT,
    failures        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (stream, sha256)
);

CREATE TABLE uploads (
    id         TEXT COLLATE "C" PRIMARY KEY,
    backend    TEXT NOT NULL DEFAULT '',
    repo_key   TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL DEFAULT '',
    state      BYTEA,
    bytes      BIGINT NOT NULL DEFAULT 0,
    started_at BIGINT,
    updated_at BIGINT
);
CREATE INDEX uploads_updated ON uploads (updated_at, id);

CREATE TABLE api_replays (
    scope       TEXT COLLATE "C" NOT NULL,
    key_hash    TEXT COLLATE "C" NOT NULL,
    fingerprint TEXT NOT NULL,
    resource_id BIGINT NOT NULL,
    expires_at  BIGINT NOT NULL,
    PRIMARY KEY (scope, key_hash)
);
CREATE INDEX api_replays_expiry ON api_replays (expires_at, scope, key_hash);

-- Every request budget admission locks the guard's one permanent row, so
-- admissions and the table's capacity check run one at a time (decision
-- 0043). The row belongs to the schema, which is why the baseline writes it.
CREATE TABLE request_budget_guard (
    id BIGINT PRIMARY KEY CHECK (id = 1)
);
INSERT INTO request_budget_guard (id) VALUES (1);

CREATE TABLE request_budgets (
    key_hash  TEXT COLLATE "C" PRIMARY KEY,
    used      BIGINT NOT NULL CHECK (used >= 0),
    resets_at BIGINT NOT NULL
);
CREATE INDEX request_budgets_expiry ON request_budgets (resets_at, key_hash);
-- +down
DROP TABLE request_budgets;
DROP TABLE request_budget_guard;
DROP TABLE api_replays;
DROP TABLE uploads;
DROP TABLE failed_blob_fetches;
DROP TABLE applied_markers;
DROP TABLE peer_cursors;
DROP TABLE outbox;
DROP TABLE outbox_sequences;
DROP TABLE instance;
DROP TABLE config_secrets;
DROP TABLE config_documents;
DROP TABLE work_items;
DROP TABLE leases;
DROP TABLE settings;
