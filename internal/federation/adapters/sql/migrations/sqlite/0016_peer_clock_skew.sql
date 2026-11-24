-- A peer record keeps the clock skew its last verified status read measured
-- (#1949, decision 0021, Wire contract): the peer's clock minus this
-- instance's, in milliseconds. 0015 belongs to FED-02's item stamps.
-- +up
ALTER TABLE peers ADD COLUMN clock_skew_ms BIGINT NOT NULL DEFAULT 0;
-- +down
ALTER TABLE peers DROP COLUMN clock_skew_ms;
