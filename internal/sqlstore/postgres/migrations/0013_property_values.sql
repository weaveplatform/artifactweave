-- Property values the B-tree entry limit (2704 bytes) cannot hold. The stores
-- keep values up to types.PropertyValueMax, 4000 UTF-16 code units or up to
-- 12 KB of UTF-8, but the keys of item_properties and build_properties held
-- the whole value, so a longer one failed to write.
--
-- Uniqueness moves to md5(value): an index expression must be IMMUTABLE, and
-- sha256 needs convert_to, which is only STABLE. Each write replaces every
-- value of a key, so the index only folds duplicates within one write; a
-- crafted collision can drop one of two values the same caller sent, nothing
-- more.
--
-- Lookups by value use the first 255 characters (at most 1020 bytes), as
-- Artifactory's own PostgreSQL index did from its 3.1.1 conversion; pkg/aql
-- narrows on that prefix (valuePrefixLen), then compares the whole value.
--
-- SQLite has no entry limit, so its schema keeps the whole value in its keys.
-- +up
ALTER TABLE item_properties DROP CONSTRAINT item_properties_pkey;
DROP INDEX item_properties_value;
CREATE UNIQUE INDEX item_properties_unique ON item_properties (item_id, key, md5(value));
CREATE INDEX item_properties_value ON item_properties (key, left(value, 255));
ALTER TABLE build_properties DROP CONSTRAINT build_properties_pkey;
DROP INDEX build_properties_value;
CREATE UNIQUE INDEX build_properties_unique ON build_properties (build_id, module_position, key, md5(value));
CREATE INDEX build_properties_value ON build_properties (key, left(value, 255));
-- +down
-- The old keys hold the whole value, so the rows they cannot index stay
-- behind. Each table is rebuilt rather than cleared in place, because an
-- index built in the transaction that deleted a row still indexes that row.
CREATE TABLE build_properties_0012 (
    build_id        BIGINT NOT NULL,
    module_position INTEGER NOT NULL,
    key             TEXT COLLATE "C" NOT NULL,
    value           TEXT COLLATE "C" NOT NULL,
    CONSTRAINT build_properties_pkey PRIMARY KEY (build_id, module_position, key, value),
    CONSTRAINT build_properties_build_id_fkey FOREIGN KEY (build_id)
        REFERENCES builds (id) ON DELETE CASCADE
);
INSERT INTO build_properties_0012
    SELECT build_id, module_position, key, value FROM build_properties
    WHERE octet_length(key) + octet_length(value) <= 2600;
DROP TABLE build_properties;
ALTER TABLE build_properties_0012 RENAME TO build_properties;
CREATE INDEX build_properties_value ON build_properties (key, value);
CREATE TABLE item_properties_0012 (
    item_id BIGINT NOT NULL,
    key     TEXT COLLATE "C" NOT NULL,
    value   TEXT COLLATE "C" NOT NULL,
    CONSTRAINT item_properties_pkey PRIMARY KEY (item_id, key, value),
    CONSTRAINT item_properties_item_id_fkey FOREIGN KEY (item_id)
        REFERENCES items (id) ON DELETE CASCADE
);
INSERT INTO item_properties_0012
    SELECT item_id, key, value FROM item_properties
    WHERE octet_length(key) + octet_length(value) <= 2600;
DROP TABLE item_properties;
ALTER TABLE item_properties_0012 RENAME TO item_properties;
CREATE INDEX item_properties_value ON item_properties (key, value);
