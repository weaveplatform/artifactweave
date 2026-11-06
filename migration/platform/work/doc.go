// Package work runs the persistent work queue (decision 0033): a pool of
// workers that lease due items, run the handler registered for each item's
// kind, and complete, retry or park it. Work is asked for with Enqueue
// inside the transaction of the change that needs it; readers that must see
// the work done call EnsureFresh first.
//
// The package also declares the store port the queue persists through:
// WorkQueue, the queue table, with its rows and bounds, and LeaseStore, the
// named leases that keep work from running twice at once.
//
// # Role
//
// platform layer: the persistent work queue and its store port. Background
// work belongs to no module, so the queue sits in the platform. The
// composition root builds one per node and runs it; the item service
// registers each format's derived work, the reindex kinds and cache
// maintenance on it; the server's reindex and Debian snapshot operations
// enqueue through it; and the remote repositories run their Smart Remote and
// Maven companion work on queues of their own over the same table. The port
// and its rows are declared here, by the package that persists through them,
// and New takes Store, the two methods the queue calls, so that the package
// imports no store (decision 0044, "Narrowing"); the metadata store
// satisfies it. The SQL and in-memory stores implement WorkQueue and
// LeaseStore, and what must not run twice at once, such as the blob
// mirror's repair and the NuGet and Nix publication locks, takes a Lease
// through LeaseStore. The package imports the kernel, the OpenTelemetry
// metric API and the standard library.
//
// # Inputs
//
//   - New: a Store, whose WorkQueue() is the queue table and whose
//     AfterCommit runs a function once the caller's transaction commits;
//     and Options: Workers (4), Lease (two minutes), MaxAttempts (10), Poll
//     (one second), Holder (the host name, process id and a random suffix),
//     Backoff (Backoff), Now, Log and Meters (no metrics when nil). The
//     composition root sets the first three from work.workers, work.lease
//     and work.max_attempts.
//   - The handlers, registered per kind before Run: Handle for work that
//     runs as soon as it is due, HandleEvery for periodic work that waits
//     for a timer.
//   - Enqueue's kind, key and payload (a small JSON document), called with
//     the context of the caller's transaction; EnsureFresh's kind and key.
//
// # Outputs
//
//   - Queue, with Handle, HandleEvery, Enqueue, Run, RunDue, EnsureFresh and
//     WaitIdle; Handler, Options, Backoff and the defaults; the sentinels
//     ErrPermanent, ErrParked, ErrNoHandler, ErrOptions, ErrDuplicateHandler
//     and ErrPanic.
//   - The port: Store; WorkQueue, with WorkLease, WorkFailure, the bounds
//     MaxWorkKind, MaxWorkKey and MaxWorkPayload, and ValidateWork and
//     ValidateLease, which every adapter applies; LeaseStore.
//   - The rows: WorkItem, WorkState with WorkPending, WorkLeased and
//     WorkParked, WorkStats, and Lease with Held.
//   - Metrics through Options.Meters: artifactweave.work.items by kind and
//     state, artifactweave.work.oldest_pending by kind, in seconds, and
//     artifactweave.work.runs by kind and outcome (completed, failed,
//     parked, released, lost).
//
// # Invariants
//
//   - Work is enqueued inside the caller's transaction, so it is asked for
//     exactly when the change that needs it commits and never after a
//     rollback; this node's workers are woken once it has committed.
//   - One item per kind and key: work asked for again while the item waits
//     joins it, and work asked for during a run runs once more after it.
//   - A run holds its item by a lease and a generation, and extends the
//     lease every half lease. A run whose lease is lost is cancelled and
//     recorded as lost, and its generation can no longer complete or fail
//     the item.
//   - A failed run is retried after Backoff, and parked once the item has
//     used MaxAttempts or the handler returns ErrPermanent, until the key is
//     enqueued again; a panic counts as a failure. A run cut short because
//     the queue stops is handed back without counting the attempt and runs
//     again, so handlers must be idempotent.
//   - Kinds are code constants, so the kind label of every metric stays
//     bounded (decision 0016).
//   - The meter scope is the package's path after promotion,
//     github.com/weaveplatform/artifactweave/internal/platform/work, set
//     once in P9d (decision 0044), so dashboards and alert rules that select
//     on it change once.
//   - Every port method is given the time it acts at, so no adapter reads a
//     clock.
//
// # References
//
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the queue, leasing, retries and parking, periodic kinds, its
//     configuration and its metrics).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md,
//     line 75 (the queue's OpenTelemetry meter, with no-op defaults and
//     recorder tests).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Narrowing"; "Type-alias bridges": the metadata store and the shared
//     types keep aliases of the port and the rows while their users move;
//     the meter scope, set once to the final path).
//   - Research 15: docs/research/15-data-model.md, lines 697 and 698 (the
//     leases and work_items tables).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 144 (platform/work) and line 1039 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, #p7-npm-go-maven-locals
//     and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/297
//   - https://github.com/weaveplatform/artifactweave/issues/576
//   - https://github.com/weaveplatform/artifactweave/issues/985
//
// # Status
//
// Implemented P2: LeaseStore and Lease, the named leases on background work
// (pull request 3). P7: the persistent work queue with its port and rows,
// EnsureFresh and the metrics (issue 297, pull request 299), and periodic
// kinds, HandleEvery and RunDue, for maxUniqueSnapshots (pull request 512).
// P8: a pass holds an item it meets again until the pass ends (issue 576,
// pull request 577). P9: ErrPermanent, for the Maven remotes' durable
// companion work (pull request 657). Staged in P9d: New takes Store, and the
// meter scope is the final path.
//
// # Testing
//
//   - work_test.go: Backoff and New's options; runs that complete once their
//     transaction commits, work asked for twice that runs once, and nothing
//     after a rollback; retries and parking, a panic, a run handed back when
//     the queue stops, heartbeats and a lost lease, EnsureFresh, WaitIdle's
//     deadline, Run without handlers, a failing store and a permanent
//     refusal; TestUnitMeterName: every artifactweave.work.* metric is
//     reported under the final scope.
//   - periodic_test.go: periodic kinds, their timer and RunDue, with items
//     asked for again during their run, ties on a clock that stands still,
//     a pass cut short, a held item whose lease runs out, and a hand-back or
//     a store that fails.
//   - types_test.go: TestUnitLeaseHeld.
//
// The metadata store's work and lease contracts (the Work* subtests:
// validation, lifecycle, coalescing, fencing, failures, LeaseKey,
// transactions and concurrency; and the Leases subtest) run on its
// in-memory, SQLite and PostgreSQL stores. The tests beside the store's and
// the shared types' bridges fail if an alias of the port or the rows
// becomes a declared copy.
package work
