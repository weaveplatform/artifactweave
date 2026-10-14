-- How a token's subject resolves to a user (types.TokenIdentity): an OIDC
-- token exchange may issue a token for a user who is not stored (research 21).
-- +up
ALTER TABLE tokens ADD COLUMN identity TEXT NOT NULL DEFAULT '';
-- +down
ALTER TABLE tokens DROP COLUMN identity;
