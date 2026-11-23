// Package schedule evaluates bounded Quartz expressions and persists logical
// work occurrences through the platform's existing transactional work queue.
//
// # Role
//
// This platform foundation is shared by cleanup, single replication and
// garbage collection (gcConfig.cronExp, artifacts/core/gc). It
// imports no business module. Owners update a schedule snapshot in the same
// transaction as their configuration, after their own domain guard. The order
// is owner/domain state, schedule row, then queue row; no owner lookup occurs
// under a schedule lock. A schedule lease is unnecessary for correctness:
// locked position/revision equality and atomic advancement serialize ticks.
//
// # Inputs
//
// Quartz expressions and named timezones, owner references/configuration,
// injected clocks, shared transactional stores and durable queue lease items.
//
// # Outputs
//
// Parsed expressions and next-fire times; versioned schedule snapshots;
// distinct durable Trigger occurrences; bounded polling and admitted handlers.
// Invalid input wraps kernel.ErrInvalid; expiry returns ErrNoFuture.
//
// # Invariants
//
// Parse accepts Quartz six/seven-field syntax, including seconds, named ranges,
// lists, increments (a bare "/n" is "*/n", as in Quartz and Artifactory's
// default "0 0 /4 * * ?"), ?, L, L-offset, W, LW and #. Exactly one day field is ?.
// The published 1970–2099 year range bounds all searches. Real calendar dates
// prevent invalid February days from normalizing into March.
//
// An empty timezone means UTC. Explicit IANA locations support civil schedules;
// the process-dependent Local location is refused. Nonexistent civil times are
// skipped, and a repeated civil time fires only at its first absolute instant.
// These timezone/DST choices are project policy, not a vendor runtime claim.
//
// Missed fires catch up from the persisted logical time, one occurrence per
// selected schedule per bounded Tick. Repeated ticks and restarts cannot enqueue
// an advanced occurrence again. Distinct fires have distinct queue keys, so the
// existing queue's coalescing cannot lose logical occurrences. Completed queue
// rows may disappear: the schedule position remains the deduplication record.
//
// Configure retains an unchanged snapshot's position/revision. Edits and
// enablement changes get a fresh opaque revision; recreation never reuses an old
// one. Execute rechecks current existence/enabled/revision and lease generation
// before calling a business handler outside the admission transaction. Already
// admitted jobs keep the queue's at-least-once semantics; owners fence later
// publication points when a running job needs to observe subsequent changes.
// Execute supplies a transient Trigger.Fence for durable owner admission. Call
// it last in the owner's transaction, after domain writes, so an expired lease
// rolls back the run and any child queue item. Never persist that callback or
// acquire another domain lock after it.
// Existing HandleEvery maintenance jobs remain independent.
//
// # References
//
//   - docs/research/decisions/0010-retention-trash-and-gc.md
//   - docs/research/decisions/0033-persistent-work-queue.md
//   - docs/research/decisions/0044-modular-monolith-migration.md
//   - https://github.com/weaveplatform/artifactweave/issues/1755
//   - https://www.quartz-scheduler.org/documentation/quartz-2.3.0/tutorials/crontrigger.html
//
// # Status
//
// Implemented for P11 CLN-01; P12 CFG-05 moved garbage collection onto it
// and accepted the bare increment
// (https://github.com/weaveplatform/artifactweave/issues/1829). Cleanup and
// replication owners consume this
// foundation in their execution tickets; this package does not activate their
// public APIs or replace unrelated fixed-interval maintenance jobs.
//
// # Testing
//
// Calendar/DST tests cover published syntax, leap days, end-of-month rules,
// missing ordinal weekdays, expiry, invalid input and concurrent evaluation.
// The shared platform store contract runs actual memory/SQLite/PostgreSQL
// positions, concurrent ticks, restart, rollback, stale revisions and queue
// generation fencing. Migration tests preserve existing work/event consumers.
package schedule
