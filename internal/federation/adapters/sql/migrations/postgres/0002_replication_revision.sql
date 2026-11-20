-- Stable public replication keys must not revive an old worker after edits
-- or deletion/recreation. Go allocates private random revisions on upgrade.
-- +up
ALTER TABLE replications ADD COLUMN private_revision TEXT NOT NULL DEFAULT '';
-- +down
ALTER TABLE replications DROP COLUMN private_revision;
