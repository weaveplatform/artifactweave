-- Stream delivery (PEER-04, decision 0021): how a peer's entries travel,
-- pushed by default or pulled when one instance of the pair is behind NAT,
-- and the receivers that fell behind an origin's pruned floor on a stream
-- and need a full sync (PEER-05). One of origin and receiver is this
-- instance; position is the receiver's applied or acknowledged sequence
-- when the gap was found. 0011 belongs to PEER-02's pairing redeemer.
-- +up
ALTER TABLE peers ADD COLUMN delivery TEXT NOT NULL DEFAULT 'push';
CREATE TABLE stream_resyncs (
    origin     TEXT NOT NULL,
    receiver   TEXT NOT NULL,
    stream     TEXT NOT NULL,
    position   BIGINT NOT NULL DEFAULT 0,
    flagged_at BIGINT,
    PRIMARY KEY (origin, receiver, stream)
);
CREATE INDEX stream_resyncs_receiver ON stream_resyncs (receiver, origin, stream);
-- +down
DROP INDEX stream_resyncs_receiver;
DROP TABLE stream_resyncs;
ALTER TABLE peers DROP COLUMN delivery;
