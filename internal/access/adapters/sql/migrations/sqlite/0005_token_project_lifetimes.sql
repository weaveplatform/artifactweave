-- +up
-- Existing role scopes granted nothing before PRJ-02. Leave them unbound;
-- assigning today's lifetime during upgrade could revive an old delegation.
ALTER TABLE tokens ADD COLUMN project_incarnations TEXT NOT NULL DEFAULT '{}';

-- +down
ALTER TABLE tokens DROP COLUMN project_incarnations;
