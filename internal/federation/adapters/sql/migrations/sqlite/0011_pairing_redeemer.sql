-- A pairing nonce records who redeemed it, the redeeming instance and the
-- fingerprint of the key it presented (PEER-02, decision 0021), so a repeat
-- of a redeem that succeeded answers again while the token lasts and any
-- other redeemer is refused. 0010 belongs to PEER-03's request nonces.
-- +up
ALTER TABLE pairing_nonces ADD COLUMN redeemed_by TEXT NOT NULL DEFAULT '';
-- +down
ALTER TABLE pairing_nonces DROP COLUMN redeemed_by;
