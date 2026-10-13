-- The private key of a client certificate, apart from the certificate's PEM
-- and sealed like every other private key (decision 0017). Certificates
-- stored before this migration keep their bundle in pem.
-- +up
ALTER TABLE certificates ADD COLUMN private_key BYTEA;
-- +down
ALTER TABLE certificates DROP COLUMN private_key;
