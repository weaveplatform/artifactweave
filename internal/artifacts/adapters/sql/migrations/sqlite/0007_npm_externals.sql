-- Rendered dependency tags must retain their original URL after restart.
-- +up
CREATE TABLE npm_externals (
 virtual_key TEXT NOT NULL REFERENCES repositories(key) ON DELETE CASCADE,
 name TEXT NOT NULL,
 spec TEXT NOT NULL,
 source_url TEXT NOT NULL,
 file TEXT NOT NULL,
 sha256 TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (virtual_key, name, spec)
);
-- +down
DROP TABLE npm_externals;
