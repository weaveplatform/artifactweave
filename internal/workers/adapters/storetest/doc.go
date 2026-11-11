// Package storetest is the contract suite of the workers module's stores:
// one set of expectations that every implementation of the worker store
// must meet.
//
// # Role
//
// workers module, adapter layer, test support. The module's SQL and
// in-memory adapters run Run with a Factory that returns a fresh store; the
// metadata store's contract runs it too, so its in-memory, SQLite and
// PostgreSQL stores pass the same cases. The package imports the standard
// library, the kernel, the platform and the module's core only, and
// artifacts/api/repo, which the workers model's filter names.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty port.Store and registers
//     its cleanup with t.
//
// # Outputs
//
//   - Factory and Run.
//   - The subtests, one per concern, each on a store of its own: Workers,
//     WorkerState, WorkerSecrets and WorkerExecutions.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls Run where it ran them, so its runs list the same
//     subtests.
//
// # References
//
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Factory and Run).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 694 (the workers adapters), line 847 (store contract suites per
//     module, run by its adapters) and line 1057 (the metadata store's
//     contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the worker cases, in the metadata store's contract (pull
// request 3). Moved here in P9d, which added Factory and Run.
//
// # Testing
//
// The package is the suite. workers/adapters/inmem's TestUnitContract and
// workers/adapters/sql's TestAccContract (SQLite in memory and in a file)
// run Run, and so do the metadata store's TestUnitContract,
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL).
package storetest
