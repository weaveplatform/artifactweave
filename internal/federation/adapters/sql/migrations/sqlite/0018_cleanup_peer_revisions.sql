-- FED-03 reserves 0017. A pre-existing peer's initial marker belongs to that
-- existing relationship; every subsequent Put allocates a fresh grant marker.
-- +up
ALTER TABLE peers ADD COLUMN grant_revision TEXT NOT NULL DEFAULT 'initial' CHECK (grant_revision <> '');
ALTER TABLE peers ADD COLUMN failure_revision TEXT NOT NULL DEFAULT 'initial' CHECK (failure_revision <> '');
CREATE TABLE federated_cleanup_intents (
    id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision > 0),
    document TEXT NOT NULL CHECK (LENGTH(document) <= 4194304)
);
-- +down
DROP TABLE federated_cleanup_intents;
ALTER TABLE peers DROP COLUMN failure_revision;
ALTER TABLE peers DROP COLUMN grant_revision;
