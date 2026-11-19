-- +up
-- General module events share outbox sequencing; peer cursor semantics remain separate.
ALTER TABLE outbox_sequences ADD COLUMN pruned_through BIGINT NOT NULL DEFAULT 0;

CREATE TABLE outbox_consumers (
    origin    TEXT NOT NULL,
    stream    TEXT NOT NULL,
    consumer  TEXT NOT NULL,
    acked_seq BIGINT NOT NULL CHECK (acked_seq >= 0),
    PRIMARY KEY (origin, stream, consumer),
    FOREIGN KEY (origin, stream) REFERENCES outbox_sequences(origin, stream)
);

-- +down
DROP TABLE outbox_consumers;
ALTER TABLE outbox_sequences DROP COLUMN pruned_through;
