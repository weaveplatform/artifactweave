-- Go writes Unicode-lowercased names so list filtering is independent of
-- database locale. Open backfills existing rows in bounded transactions.
-- +up
ALTER TABLE users ADD COLUMN search_name TEXT COLLATE "C";
ALTER TABLE user_groups ADD COLUMN search_name TEXT COLLATE "C";
CREATE INDEX users_search_backfill ON users (name) WHERE search_name IS NULL;
CREATE INDEX groups_search_backfill ON user_groups (name) WHERE search_name IS NULL;
-- +down
DROP INDEX groups_search_backfill;
DROP INDEX users_search_backfill;
ALTER TABLE user_groups DROP COLUMN search_name;
ALTER TABLE users DROP COLUMN search_name;
