-- The redeemer of receiving-only trust receives from the issuer; it never
-- sends federation streams to it, regardless of repository membership.
-- +up
ALTER TABLE peers ADD COLUMN receives_federation_only BOOLEAN NOT NULL DEFAULT FALSE;
-- +down
ALTER TABLE peers DROP COLUMN receives_federation_only;
