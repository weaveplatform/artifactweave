-- +up
-- Display names otherwise inherit the database locale rather than the
-- bytewise ordering used by keys, SQLite and the reference adapter.
CREATE INDEX projects_by_name ON projects (display_name COLLATE "C", key);

-- +down
DROP INDEX projects_by_name;
