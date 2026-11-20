// Package events dispatches durable module events to named topic consumers
// through the existing persistent work queue.
//
// # Role
//
// platform layer: infrastructure with no business rules. It imports no
// module, so modules subscribe to each other's events without importing each
// other.
//
// # Inputs
//
// New takes the shared transactional Store, its work.Queue and Options for the
// local origin, bounded batch and poll interval. Subscribe binds literal topics
// to bounded code-owned consumer names, not runtime webhook/tenant identifiers.
// RegisterWork freezes subscriptions and atomically registers every durable
// position before the application starts the queue. It can be retried after a
// storage failure; restart keeps positions and reactivates registered work.
//
// # Outputs
//
// Each consumer has an independent periodic queue kind and contiguous cursor.
// An unmatched topic is acknowledged without invoking a handler. One failed
// consumer retains its position and uses the queue's backoff/parking while other
// consumers progress. Successful and empty polls retain their dispatch row, so
// generation fencing survives retries and process restarts.
//
// A Handler commits database effects or a self-contained consumer-owned handoff
// in the event transaction. The bus settles the composition's pending local
// publication work before Ack, then locks the current dispatch generation with
// WorkQueue.GuardLease and checks a fresh clock after any lock
// wait. A stale/expired lease, acknowledgement conflict or handler failure rolls
// back effects and acknowledgement together. Callbacks must not perform network
// I/O or acquire any dispatch polling row; all domain/delivery locks precede Ack.
// Publication checks also precede poll rescheduling's lease fence, so settlement
// cannot commit effects after an expiry checked against an earlier clock.
// This does not promise exactly-once remote effects. A bare outbox reference is
// insufficient for a handoff: pruning can remove it after acknowledgement.
//
// DeliveryKey identifies consumer/origin/sequence independently of the entity
// key, preserving distinct same-artifact events through derived-work coalescing.
// Pruning is capped by every registered consumer. With no consumers, no dummy
// acknowledgement discards events. Queue cancellation returns unfinished work
// without counting an attempt. The bus owns no additional goroutine lifecycle.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 142-143, and "Webhooks and events (P11, P13)", lines
//     717-744.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - Context map: platform has no row; the consumers are the notifications,
//     federation and workers rows
//     (https://github.com/weaveplatform/artifactweave/issues/958).
//   - Decision 0011: docs/research/decisions/0011-webhooks.md, events emitted
//     at the state change so that every consumer shares one source.
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// P11 EVT-03 supplies subscription, transactional dispatch, recovery and safe
// pruning. Memory and real SQLite/PostgreSQL tests cover independent consumers,
// poison parking, crash after committed Ack, takeover/expiry rollback, maximum
// payload handoff and shutdown. Vendor webhook serialization/delivery remains
// P19a; operator replay and queue administration remain P38.
package events
