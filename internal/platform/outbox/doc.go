// Package outbox holds committed module events, independent consumer positions,
// the ordered event streams that peers exchange and their hybrid logical clock.
// OutboxStore stores this
// instance's outbox, each peer's acknowledged position in it, the receiver's
// applied positions and the blob fetches that failed while applying, with the
// values it takes and returns.
//
// # Role
//
// platform layer: the ordered event streams of decision 0021 and the clock
// that orders them. The target layout puts the outbox in the platform, so
// that a module appends its events in its own transaction and a module's api
// package may import it. OutboxStore takes and returns the clock and the
// values, so they are declared beside it and the package imports no module.
// The platform's store adapters (platform/store/sql and platform/store/inmem)
// implement OutboxStore and EventStore; the composite metadata store forwards
// both views. Stage uses platform/tx before-commit finalization so domain locks
// precede the event-stream append lock. ActorFrom copies only public actor facts.
// The package imports the standard library, kernel sentinels and platform/tx.
//
// # Inputs
//
//   - Entries appended inside the transaction of the change they record:
//     origin, stream, clock time, event type, payload and, for a content
//     event, the blob's SHA-256 and size.
//   - Peers' acknowledged positions, the receiver's applied positions and
//     the blob fetches that failed while applying.
//   - For the clock, a physical time (Next and Merge) or the text form
//     (ParseHLC).
//   - Module events: origin, topic, entity key, timestamp and bounded JSON.
//     The producing module's api owns its typed topic and payload contract.
//
// # Outputs
//
//   - OutboxStore: Append, Read, LastSeq, Streams and Prune for the entries;
//     PutCursor, Cursor, Cursors and DeleteCursors for peers' positions;
//     Applied and SetApplied for the receiver's; and RecordFailedFetch,
//     FailedFetches and ClearFailedFetch for failed blob fetches.
//   - HLC, with Compare, Next, Merge and String, and ParseHLC with its
//     sentinel ErrInvalidHLC.
//   - OutboxEntry; PeerCursor, with Behind; AppliedMarker, with Expect; and
//     FailedBlobFetch.
//   - EventStore: transactional AppendEvent, idempotent RegisterConsumer,
//     bounded Next, contiguous compare-and-advance Ack and PruneAcknowledged.
//
// # Invariants
//
//   - Seq is gap-free per origin and stream, and concurrent appends to one
//     stream are serialised, never given the same Seq. An append rolled back
//     with its transaction gives its number back, and pruning never resets
//     the numbering.
//   - The HLC text form is fixed-width hex, so it sorts as Compare does and
//     can be stored in an indexed column; ParseHLC reads back every value
//     that String writes.
//   - The clock never runs behind anything it has seen: Merge takes the
//     largest wall time, and the logical counter orders events that share
//     one.
//   - SetApplied runs in the transaction that applies the entry, which makes
//     applying idempotent: a retried batch starts at the next expected
//     sequence, AppliedMarker.Expect.
//   - Module events share one reserved stream per origin and the existing
//     sequence allocator. Peer cursors and applied markers remain independent.
//   - Consumer effects and Ack share one metadata transaction. Acquire domain
//     locks before Ack; Next does not take the stream write guard. A stale Ack
//     conflicts and the caller propagates that error to roll effects back.
//   - Pruning retains every unacknowledged consumer prefix; with no registered
//     consumers it retains all events. Late consumers start at the retained
//     floor, and peer Prune cannot bypass this protection on the reserved stream.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Ordered outbox": streams, sequence numbers, acknowledgements, applied
//     positions and failed fetches; "Conflict rule": the clock time and the
//     origin), with Kulkarni et al., "Logical Physical Clocks", 2014.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules", rule 5: a module's api may import
//     platform/outbox).
//   - Research 15: docs/research/15-data-model.md, lines 703 to 706 (the
//     outbox, peer cursor, applied marker and failed fetch tables) and line
//     763 (the clock's two columns).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 140 and 141 (platform/outbox, from the store's OutboxStore,
//     generalised in P11), lines 720 to 724 (P11's Event, Append and
//     per-consumer cursors) and lines 341 and 342 (the federation module's
//     core/port, where the target layout keeps the peer types).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p13-peer-trust-and-federated-repositories and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/989
//
// # Status
//
// Implemented P2: the clock, the outbox values and OutboxStore, with the
// metadata store's SQL and in-memory stores (pull request 3). Moved here in
// P9d. P11 EVT-01 adds EventStore and independent durable consumer positions;
// EVT-02 supplies business producers and EVT-03 supplies the recovery/dispatch
// loop. P13 delivers peer streams over the network.
//
// # Testing
//
//   - outbox_test.go: TestUnitHLC (Next ahead of and behind physical time,
//     the four Merge cases, Compare and the text form agree on order,
//     ParseHLC reads back what String writes and refuses five malformed
//     values with ErrInvalidHLC) and TestUnitCursorsAndMarkers (Behind and
//     Expect).
//
// The platform's outbox contract (the Outbox, OutboxConcurrent, PeerCursors,
// AppliedMarkers and FailedFetches subtests of platform/store/storetest:
// sequences per origin and stream, reads after a sequence and up to a limit,
// pruning that keeps the numbering, a rolled-back append, concurrent appends,
// cursor replacement and deletion, applied positions per origin, failed fetch
// counting and clearing, and cancelled contexts) runs on the platform's
// in-memory and SQLite stores and on the metadata store's in-memory, SQLite
// and PostgreSQL stores. The federation model's TestUnitStamp orders stamps
// through Compare. The RunEvents contract also covers independent consumers,
// rollback of effects/acknowledgements/pruning, concurrent append/ack/register,
// origin isolation, corrupt envelopes and canceled calls. SQL-specific tests
// exercise failed migration/upgrade/rollback fixtures, injected write failures
// and the producer-domain/consumer-Ack lock order on PostgreSQL.
package outbox
