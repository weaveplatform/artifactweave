// Package sql is the SQL implementation of the workers module's stores:
// worker definitions, their state, their sealed secrets and their
// executions, with the module's registration with the SQL layer.
//
// # Role
//
// workers module, adapter layer (SQL). Store implements the module's
// port.Store (port.WorkerStore over the shared unit of work) over a shared
// dialect.Store, on SQLite and PostgreSQL alike. Module registers the
// workers stream (adapters/sql/migrations) and the sealed column
// worker_secrets.value, last in the canonical module order. The composite
// SQL store in app/adapters/sql builds one Store over its own dialect
// store, delegates Workers to it and names Module in its module list. The
// package imports the standard library, the kernel, the platform, the
// module's core and its stream only, and artifacts/api/repo, which the
// workers model's filter names.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the workers stream's
//     tables.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Workers, and the dialect store's Tx,
//     AfterCommit and Close.
//   - Module: the name workers, the stream migrations.FS and the sealed
//     column worker_secrets.value.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence").
//   - Every column the store seals is in Module: worker_secrets.value, with
//     the purpose "worker_secrets.value" and the row id "<worker>/<name>",
//     so key rotation re-seals it (decision 0017). Neither ever changes:
//     both are authenticated with every value.
//   - The per-worker limits on state are the caller's to apply
//     (model.ValidateWorkerState); the store keeps what it is given.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order;
//     "Names the migration introduces", which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 694 (the workers adapters), line 621 (the workers SQL adapter)
//     and lines 631 to 633 (RunModule).
//   - Plan: docs/implementation/plan.md#p2-stores, #p17-workers and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the worker store with its sealed secrets, as the metadata
// store's (pull request 3). Staged in P9d: Store, New and Module. The
// runtime that calls it arrives with P17.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the workers contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - workers_test.go: TestAccWorkersClosedDatabase (every call fails on a
//     closed pool), TestAccWorkersCorruptRows (a corrupt document, a
//     duration that is not a number and a secret sealed for another row
//     fail to read) and TestAccWorkersFailingStatements (a state, secret or
//     execution write fails when its INSERT does).
package sql
