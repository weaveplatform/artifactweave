-- +up
CREATE TABLE api_restriction_audit (
    id BIGSERIAL PRIMARY KEY,
    user_name TEXT COLLATE "C" NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL,
    until_at BIGINT,
    changed_at BIGINT NOT NULL
);
CREATE INDEX api_restriction_audit_user ON api_restriction_audit (user_name, id DESC);
CREATE TABLE api_restrictions (
    user_name TEXT COLLATE "C" PRIMARY KEY REFERENCES users (name) ON DELETE CASCADE,
    id BIGINT NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL,
    until_at BIGINT,
    changed_at BIGINT NOT NULL
);
-- +down
DROP TABLE api_restrictions;
DROP TABLE api_restriction_audit;
