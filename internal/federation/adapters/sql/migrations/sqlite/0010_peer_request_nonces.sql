-- The replay cache of signed peer requests (decision 0021, Transport): the
-- SHA-256 of each accepted request's keyid and nonce, kept until the
-- request could no longer pass the five-minute skew check. It is a table
-- rather than process memory so that a request accepted before a restart,
-- or by another node on the same database, is refused when replayed.
-- +up
CREATE TABLE peer_request_nonces (
    nonce_hash BLOB PRIMARY KEY,
    expires_at BIGINT NOT NULL
);
CREATE INDEX peer_request_nonces_expires ON peer_request_nonces (expires_at);
-- +down
DROP INDEX peer_request_nonces_expires;
DROP TABLE peer_request_nonces;
