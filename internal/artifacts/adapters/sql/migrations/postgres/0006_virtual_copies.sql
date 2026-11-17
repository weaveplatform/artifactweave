-- Metadata only; content remains in the reference-counted blob store.
-- +up
CREATE TABLE virtual_copies (
    virtual_key TEXT COLLATE "C" NOT NULL REFERENCES repositories(key) ON DELETE CASCADE,
    path TEXT COLLATE "C" NOT NULL,
    document TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (virtual_key, path)
);
-- +down
-- Dropping derived copies also releases their content for garbage collection.
UPDATE blobs SET ref_count = GREATEST(0, ref_count -
    (SELECT COUNT(*) FROM virtual_copies
     WHERE document::jsonb->'item'->'actual'->>'sha256' = blobs.sha256))
WHERE sha256 IN (SELECT document::jsonb->'item'->'actual'->>'sha256' FROM virtual_copies);
DROP TABLE virtual_copies;
