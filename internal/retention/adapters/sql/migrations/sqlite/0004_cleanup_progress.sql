-- +up
CREATE TABLE cleanup_progress (
    run_id BIGINT PRIMARY KEY REFERENCES cleanup_runs(id),
    revision BIGINT NOT NULL,
    snapshot TEXT NOT NULL
);
CREATE TABLE cleanup_entries (
    run_id BIGINT NOT NULL REFERENCES cleanup_runs(id),
    kind TEXT NOT NULL,
    entity_id BIGINT NOT NULL,
    version_id BIGINT NOT NULL,
    revision BIGINT NOT NULL,
    state TEXT NOT NULL,
    is_lead BOOLEAN NOT NULL,
    lead_rank BIGINT NOT NULL,
    lead_tie TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    PRIMARY KEY (run_id, kind, entity_id)
);
CREATE INDEX cleanup_pending ON cleanup_entries (run_id, version_id, state, is_lead, lead_rank, lead_tie, entity_id);
-- +down
DROP TABLE cleanup_entries;
DROP TABLE cleanup_progress;
