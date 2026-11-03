-- +up
CREATE TABLE request_budget_guard (id BIGINT PRIMARY KEY CHECK (id = 1));
INSERT INTO request_budget_guard (id) VALUES (1);
CREATE TABLE request_budgets (
    key_hash TEXT COLLATE "C" PRIMARY KEY,
    used BIGINT NOT NULL CHECK (used >= 0),
    resets_at BIGINT NOT NULL
);
CREATE INDEX request_budgets_expiry ON request_budgets (resets_at, key_hash);
-- +down
DROP TABLE request_budgets;
DROP TABLE request_budget_guard;
