-- +up
CREATE TABLE bower_registrations (
    repo_key TEXT NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    PRIMARY KEY (repo_key, name)
);
-- +down
DROP TABLE bower_registrations;
