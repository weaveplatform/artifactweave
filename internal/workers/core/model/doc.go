// Package model holds the workers module's entity values: worker
// definitions with their event filters, the limits on a worker's source and
// state, and the record of each execution.
//
// # Role
//
// workers module, core layer, entity values. The module's store port takes
// and returns them, and P17's runtime will build on them. The package
// imports the standard library and artifacts/api/repo (the repository class
// a filter selects on). WorkerFilter matches paths with a matcher of its
// own rather than the federation model's PatternFilter, because a module
// imports another only through that module's api (decision 0044, dependency
// rule 6).
//
// # Inputs
//
//   - For WorkerFilter.Matches, an event's repository key, repository class
//     and item path.
//   - For ValidateWorkerSource and ValidateWorkerState, a worker's source
//     code or its state entries.
//
// # Outputs
//
//   - Worker with Blocked, WorkerAction with Before, and WorkerFilter with
//     Matches.
//   - WorkerExecution and WorkerTrigger with its Trigger* constants.
//   - The MaxWorker* limits, ValidateWorkerSource and ValidateWorkerState,
//     and their sentinel ErrInvalidWorker.
//
// # Invariants
//
//   - The limits are JFrog's defaults (decision 0026). State limits count
//     characters, not bytes.
//   - A BEFORE_* action runs synchronously before its operation and can
//     refuse it.
//   - A filter's include patterns, when there are any, must match and its
//     exclude patterns must not: the federation model's PatternFilter rule,
//     so a change to one changes both.
//
// # References
//
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules").
//   - Research 16: docs/research/16-enterprise-products.md. Research 15:
//     docs/research/15-data-model.md, lines 722 to 725 (the workers tables)
//     and line 768 (the executions table as built).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton ahead of its phase), line 355 and
//     lines 668 to 690 (the workers module), lines 577 and 578 (each module
//     owns its store ports) and line 1058 (the shared types split into each
//     module's core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0, #p17-workers and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/957 (PLT-01,
//     which found the import of the federation model).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: the worker values among the shared domain types (pull
// request 3). Staged in P9d, where the filter took its own matcher. The
// behaviour that uses them arrives with P17.
//
// # Testing
//
//   - worker_test.go: TestUnitWorkers (Before, filter matching by
//     repository key, class and exclude pattern, Blocked, and the source
//     and state limits failing with ErrInvalidWorker).
//
// The metadata store's Workers, WorkerState, WorkerSecrets and
// WorkerExecutions contract subtests store and read these values on its
// in-memory, SQLite and PostgreSQL stores.
package model
