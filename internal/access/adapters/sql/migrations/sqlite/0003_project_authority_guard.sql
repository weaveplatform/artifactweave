-- +up
-- Project snapshots and mutations take this permanent guard before any
-- project, assignment, role, environment or membership row lock.
CREATE TABLE project_authority_guard (
    id BIGINT NOT NULL PRIMARY KEY CHECK (id = 1)
);
INSERT INTO project_authority_guard (id) VALUES (1);

-- +down
DROP TABLE project_authority_guard;
