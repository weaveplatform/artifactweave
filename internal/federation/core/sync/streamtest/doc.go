// Package streamtest is a stream kind for tests: it proves the delivery
// engine of core/sync on streams "test:<name>" without repository
// semantics, so FED-02 only plugs in repo:<key>.
//
// # Role
//
// test support for the federation module's core/sync engine: the engine's
// unit tests and the two-instance scenarios of test/server/federation
// register it in each instance's registry.
//
// # Inputs
//
//   - Subscriptions set by the test (Subscribe), and the use cases a peer
//     must hold to send or receive (Send, Receive; federated-repo/* by
//     default).
//   - Entries the engine applies, and changes the test commits through the
//     engine (Commit): put with a value and optionally content, delete.
//
// # Outputs
//
//   - streamsync.Kind: Apply records every applied sequence of an origin's
//     stream and its items in the settings store, in the apply
//     transaction; Publish makes items pending a blob visible; References
//     says which content a visible item holds.
//   - streamsync.SnapshotProvider and streamsync.Reconciler: its items, with
//     each one's clock time, origin and tombstone (State.Stamps), listed for
//     a snapshot and reconciled from one by the conflict rule.
//   - State, which a test reads to see what an instance applied, and in
//     which order; Diverge, which writes an item only this instance holds,
//     as a forced full sync discards.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Ordered outbox").
//   - https://github.com/weaveplatform/artifactweave/issues/1928 (PEER-04)
//     and https://github.com/weaveplatform/artifactweave/issues/1929
//     (PEER-05).
//
// # Status
//
// Implemented P13 (PEER-04, PEER-05).
//
// # Testing
//
// Exercised by core/sync's unit tests and test/server/federation's stream
// scenarios; its state lives in settings keys under "streamtest/", which no
// production instance writes.
package streamtest
