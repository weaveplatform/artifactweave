-- +up
-- Empty defaults allow existing rows to migrate; AfterMigrate allocates the
-- missing opaque values before the store begins serving requests.
ALTER TABLE projects ADD COLUMN incarnation TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN policy_revision TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE project_repositories ADD COLUMN sharing_revision TEXT COLLATE "C" NOT NULL DEFAULT '';

-- +down
ALTER TABLE project_repositories DROP COLUMN sharing_revision;
ALTER TABLE projects DROP COLUMN policy_revision;
ALTER TABLE projects DROP COLUMN incarnation;
