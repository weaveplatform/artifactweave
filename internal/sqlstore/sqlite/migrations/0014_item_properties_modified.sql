-- When an item's properties last changed (types.Item.PropertiesModified),
-- which the storage API lists as mdTimestamps.properties; NULL while it has
-- none. Artifactory 6.20 keeps the time in a table of its own
-- (node_meta_infos.props_modified); nothing else from that table is kept
-- here, so a column is enough. Number 0013 is PostgreSQL's alone, so this
-- one keeps the number its PostgreSQL twin has.
-- +up
ALTER TABLE items ADD COLUMN props_modified_at BIGINT;
-- +down
ALTER TABLE items DROP COLUMN props_modified_at;
