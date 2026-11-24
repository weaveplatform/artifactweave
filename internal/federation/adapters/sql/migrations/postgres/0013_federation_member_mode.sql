-- A federated repository member records its mode, bidirectional or receiver
-- (FED-01, decision 0022); '' is bidirectional, 7.x's default when the
-- repository JSON leaves mode out. 0012 belongs to PEER-04's stream
-- delivery.
-- +up
ALTER TABLE federation_members ADD COLUMN mode TEXT NOT NULL DEFAULT '';
-- +down
ALTER TABLE federation_members DROP COLUMN mode;
