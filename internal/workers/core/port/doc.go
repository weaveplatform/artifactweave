// Package port declares the workers module's store ports: the worker store
// and Store, the part of the metadata store the module uses.
//
// # Role
//
// workers module, core layer, store ports. The metadata store's SQL and
// in-memory stores implement WorkerStore, and the metadata store satisfies
// Store; STO-15 moves those implementations into the module's adapters. The
// ports take and return the module's core/model values and kernel.Page,
// and join the unit of work of platform/tx. The target layout also puts
// the runtime's Sandbox port here, which arrives with P17.
//
// # Inputs
//
//   - Workers, state entries, secrets and executions to store; keys, names
//     and ids to look up; a WorkerFilter or a kernel.Page to narrow a
//     listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - WorkerStore: Create, Get, Update, Delete and List; State, SetState
//     and DeleteState; PutSecret, Secrets and DeleteSecret; AddExecution,
//     Execution, Executions and PruneExecutions.
//   - WorkerFilter.
//   - Store: tx.Runner and Workers.
//
// # Invariants
//
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrConflict for a
//     worker key or an execution id that exists, kernel.ErrNotFound for a
//     missing worker.
//   - Deleting a worker deletes its state, secrets and executions.
//   - Secrets are sealed at rest (decision 0017).
//   - The store does not apply the per-worker state limits; the caller
//     does, with model.ValidateWorkerState.
//
// # References
//
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence" and "Names the migration introduces", which lists
//     Store).
//   - Research 15: docs/research/15-data-model.md, lines 722 to 725 (the
//     workers tables).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton holds core/port/store.go and its
//     store adapters), line 685 (the workers module's core/port), lines 577
//     and 578 (each module owns its store ports) and line 1055 (the metadata
//     store split into each module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1, #p17-workers and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: WorkerStore, with the metadata store's in-memory, SQLite
// and PostgreSQL stores (pull request 3). Staged in P9d, where Store is
// declared. The runtime that calls the port arrives with P17.
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// metadata store's Workers, WorkerState, WorkerSecrets and WorkerExecutions
// contract subtests run the port on its in-memory, SQLite and PostgreSQL
// stores, and compile-time assertions in the in-memory and SQL stores'
// tests show that both satisfy Store.
package port
