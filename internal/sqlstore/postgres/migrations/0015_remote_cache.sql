-- Durable repository lifetimes and raw upstream retrieval state. Content stays
-- in the existing blob/item stores; cache-only reads still use cached items.
-- +up
CREATE TABLE remote_sources (
    repo_key TEXT PRIMARY KEY REFERENCES repositories(key) ON DELETE CASCADE,
    doc TEXT NOT NULL
);
CREATE TABLE remote_cache (
    repo_key TEXT NOT NULL REFERENCES repositories(key) ON DELETE CASCADE,
    cache_id TEXT NOT NULL,
    path TEXT NOT NULL,
    doc TEXT NOT NULL,
    PRIMARY KEY (repo_key, cache_id)
);
INSERT INTO remote_sources (repo_key, doc)
SELECT key, '{"source":{"kind":"external-http","repository":"' || md5(random()::text || clock_timestamp()::text) || '","id":"migration","generation":"initial"},"revision":0}' FROM repositories;
-- +down
-- Releasing the cache-owned references makes the old schema's GC safe.
UPDATE blobs SET ref_count = GREATEST(0, ref_count - (
    SELECT COUNT(*) FROM remote_cache c
    WHERE (c.doc::json ->> 'item_id')::bigint = 0 AND c.doc::json -> 'blob' -> 'Checksums' ->> 'sha256' = blobs.sha256
));
DROP TABLE remote_cache;
DROP TABLE remote_sources;
