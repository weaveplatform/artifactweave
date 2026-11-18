-- Rendered dependency tags must retain their original URL after restart.
-- +up
CREATE TABLE npm_externals (
 virtual_key TEXT COLLATE "C" NOT NULL REFERENCES repositories(key) ON DELETE CASCADE,
 name TEXT COLLATE "C" NOT NULL,
 spec TEXT COLLATE "C" NOT NULL,
 source_url TEXT COLLATE "C" NOT NULL,
 file TEXT COLLATE "C" NOT NULL,
 sha256 TEXT COLLATE "C" NOT NULL DEFAULT '',
 PRIMARY KEY (virtual_key, name, spec)
);
-- +down
DROP TABLE npm_externals;
