-- Account lockout and password expiry (decision 0005): consecutive failed
-- logins for the user lock policy, when the password was last set and
-- whether an admin expired it, for the password expiration policy.
-- +up
ALTER TABLE users ADD COLUMN failed_logins INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN password_changed_at BIGINT;
ALTER TABLE users ADD COLUMN password_expired BOOLEAN NOT NULL DEFAULT FALSE;
-- +down
ALTER TABLE users DROP COLUMN password_expired;
ALTER TABLE users DROP COLUMN password_changed_at;
ALTER TABLE users DROP COLUMN failed_logins;
