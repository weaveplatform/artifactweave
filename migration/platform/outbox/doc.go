// Package outbox holds the ordered event streams that peers exchange and the
// hybrid logical clock that orders their events: the port that stores this
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
// implement OutboxStore, and the metadata store's Outbox returns theirs; no
// service appends yet. The package imports only the standard library.
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
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Ordered outbox": streams, sequence numbers, acknowledgements, applied
//     positions and failed fetches; "Conflict rule": the clock time and the
//     origin), with Kulkarni et al., "Logical Physical Clocks", 2014.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules", rule 5: a module's api may import platform/outbox;
//     "Type-alias bridges": the shared types and the metadata store keep
//     aliases of the clock, the values and the port while their users move).
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
// metadata store's SQL and in-memory stores (pull request 3). Staged in P9d.
// Roadmap: P11 generalises the outbox to Append(ctx, Event) for every
// consumer, and P13 delivers its streams to peers.
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
// and PostgreSQL stores. The shared types' TestUnitStamp orders stamps
// through Compare, and the tests beside the shared types' and the metadata
// store's bridges fail if an alias of the clock, a value, the parser, its
// sentinel or the port becomes a declared copy.
package outbox
