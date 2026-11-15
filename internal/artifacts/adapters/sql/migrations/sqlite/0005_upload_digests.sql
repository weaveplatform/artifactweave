-- The SHA-1 and MD5 an upload computed, by the content's SHA-256, for a
-- Hugging Face LFS object whose item a later commit stores (#1581): the
-- blob stores' Stat answers the SHA-256 alone, and 7.x keeps all three
-- checksums of every file. The SHA-256 names the content, so a row never
-- changes; it outlives its blob, which a later upload of the same bytes
-- would match.
-- +up
CREATE TABLE upload_digests (
    sha256 TEXT PRIMARY KEY,
    sha1   TEXT NOT NULL,
    md5    TEXT NOT NULL
);

-- +down
DROP TABLE upload_digests;
