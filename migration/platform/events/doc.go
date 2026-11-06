// Package events will dispatch the outbox's events to subscribers: a bus on
// which each consumer subscribes to topics, with a dispatch loop driven by
// the work queue.
//
// # Role
//
// platform layer: infrastructure with no business rules. It imports no
// module, so modules subscribe to each other's events without importing each
// other.
//
// # Inputs
//
// None yet. Planned: the events that modules append to platform/outbox in
// their own transactions, read through per-consumer cursors, and
// platform/work, which drives the dispatch loop (target layout, lines 719-728
// and 742-744).
//
// # Outputs
//
// None yet. Planned (target layout, lines 725-728):
//
//   - bus.go: Subscribe(topic, consumer, Handler) and the dispatch loop.
//   - idempotency.go: consumer-side dedupe by consumer and sequence.
//   - replay.go: replay from a sequence for operators, with the queue
//     administration of P38.
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
// Placeholder: implemented by P11.
package events
