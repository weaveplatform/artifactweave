// Package storetest is the contract suite of the stores the platform owns:
// one set of expectations that every implementation of the work queue,
// leases, settings, configuration documents and the instance identity must
// meet.
//
// # Role
//
// platform layer, test support. Store declares the platform's part of the
// metadata store, every store the platform owns, here because no
// platform/store package exists to declare it. The platform's SQL and
// in-memory adapters run Run with a Factory that returns a fresh store with
// the given watches; the metadata store's contract runs RunWithoutWatches,
// because its stores register watches of their own. The package imports
// the standard library, the kernel and platform packages only.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty Store whose config store
//     calls the watches it is given, and registers its cleanup with t.
//   - RunWithoutWatches: a function that returns a fresh, empty Store.
//
// # Outputs
//
//   - Store: tx.Runner with Settings, Leases, WorkQueue, Configs and
//     Instance.
//   - Factory, Run and RunWithoutWatches.
//   - The subtests, one per concern, each on a store of its own:
//     Settings, Leases, Configs, WorkValidation, WorkLifecycle,
//     WorkCoalescing, WorkFencing, WorkFailures, WorkLeaseKey,
//     WorkTransactions, WorkConcurrency and Instance; and, from Run only,
//     ConfigWatches.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls RunWithoutWatches where it ran them, so its
//     runs list the same subtests.
//   - ConfigWatches needs a store whose only watches are its own, so only
//     Run registers it.
//   - The settings cases store values with the JSON form of the access user
//     lock policy and the artifacts trash can, declared here, because the
//     platform imports no module.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Store, Factory, Run and RunWithoutWatches).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 847 (store contract suites per module, run by its adapters).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1056
//
// # Status
//
// Implemented P2: the settings, lease, configuration document and instance
// cases, in the metadata store's contract (pull request 3). P7: the work
// queue cases (issue 297, pull request 299). Staged in P9d: Store, Factory,
// Run, RunWithoutWatches and ConfigWatches.
//
// # Testing
//
// The package is the suite. platform/store/inmem's TestUnitContract and
// platform/store/sql's TestAccContract (SQLite in memory and in a file) run
// Run; the metadata store's TestUnitContract, TestAccContractFile,
// TestAccContractMemory and TestAccContract (PostgreSQL) run
// RunWithoutWatches.
package storetest
