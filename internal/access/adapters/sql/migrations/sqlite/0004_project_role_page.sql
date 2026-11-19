-- +up
CREATE INDEX projects_by_name ON projects (display_name, key);

-- +down
DROP INDEX projects_by_name;
