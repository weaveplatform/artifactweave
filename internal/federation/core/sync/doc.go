// Package sync delivers outbox entries between peers (decision 0021,
// Ordered outbox): the sender's dispatchers and acknowledged cursors, the
// receiver's in-order, exactly-once apply, pull mode for an instance behind
// NAT, blob fetch with failed binaries, and retention pruning that flags
// receivers left behind for a full sync.
//
// # Role
//
// federation module, core layer. It shares its name with the standard
// library's sync, so a file that needs both imports that one as gosync.
// It is generic over stream kinds: a Kind says who exchanges its streams
// and applies their entries, and FED-02 registers repo:<key>.
//
// # Inputs
//
//   - Store: the peer records, the outbox with its cursors, applied markers
//     and failed fetches, the full-sync flags and this instance's identity,
//     in one unit of work.
//   - Registry: the stream kinds, each a Kind with its use cases per
//     Direction, its streams and subscribers, the content its streams
//     refer to, and its Applier.
//   - Remote: the signed peer client's push, pull and blob fetch
//     (adapters/peer); blob.Store for content; the work queue.
//   - Batches, pull requests and blob requests from verified peers, through
//     the peer API's handlers (adapters/peer).
//   - Retention, the window after which entries are pruned even if not
//     acknowledged (federation.outbox_retention, seven days by default), and
//     Priority, FED-04's dispatch order.
//
// # Outputs
//
//   - Append, which a producer calls in its change's transaction: the entry
//     with its sequence and clock time, and the dispatchers it starts.
//   - Dispatch (SendKind), PullFrom (PullKind), RetryFetch (FetchKind),
//     Sweep (SweepKind) and Prune (PruneKind) on the durable work queue,
//     registered by Register.
//   - Receive, Events and Blob for the peer API's POST and GET
//     streams/{stream}/events and GET blobs/{sha256}, with ErrNoStream (404),
//     ErrRefused (403), *ConflictError (409), ErrGone (410) and
//     kernel.ErrInvalid (400).
//   - Acknowledged cursors (FED-05 reads lag from them), failed binaries
//     (FED-06 lists and replays them through RetryFetch) and full-sync flags
//     (PEER-05 starts from them).
//
// # Invariants
//
//   - No metadata transaction spans network I/O: blobs are fetched before
//     the apply transaction, and pushes and pulls run outside one.
//   - Each entry applies in its own transaction with its applied position,
//     read with LockApplied, so a retried or concurrent batch has no second
//     effect and a stopped receiver resumes where its last commit left it.
//     The peer's grant is checked again in that transaction.
//   - A batch that does not start at the expected sequence is refused with
//     it, and the sender resumes from there; a cursor never passes the
//     stream's last entry.
//   - A failed blob fetch records a failed binary and queues its retry in
//     the apply transaction; the position still advances, so later entries
//     apply. The kind keeps the item pending, and Publish shows it unless a
//     later entry deleted or replaced it.
//   - Pruning keeps every entry a subscribed peer has not acknowledged
//     inside the retention window. A peer whose position falls below the
//     pruned floor is flagged, never sent a batch with a gap.
//
// # References
//
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Ordered outbox", "Wire contract", "Constraints").
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 336, and "Webhooks and events (P11, P13)", lines 736-739.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//   - https://github.com/weaveplatform/artifactweave/issues/1928 (PEER-04).
//
// # Status
//
// Implemented P13 (PEER-04). Snapshot full sync is PEER-05's; repository
// payloads and their apply rules are FED-02's.
//
// # Testing
//
// The package's tests run two engines over in-memory stores joined by a
// fake remote that calls the other engine directly, with the streamtest
// kind: ordered exactly-once apply through duplicated, retried and gapped
// batches, dropped answers and concurrent appliers; pull mode; blob fetch
// with missing, corrupt and late content; refusals; pruning and its flags.
// test/server/federation runs the same over two runtimes and their real
// routers on SQLite and PostgreSQL.
package sync
