// Package storetest is the contract suite of the retention module's stores:
// one set of expectations that every implementation of the cleanup and
// archive stores must meet.
//
// # Role
//
// retention module, adapter layer, test support. The module's SQL and
// in-memory adapters run Run with a Factory that returns a fresh store; the
// metadata store's contract runs it too, so its in-memory, SQLite and
// PostgreSQL stores pass the same cases. The package imports the standard
// library, the kernel, the platform and the module's core and api only.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty port.Store and registers
//     its cleanup with t.
//
// # Outputs
//
//   - Factory and Run.
//   - The subtests, one per concern, each on a store of its own:
//     CleanupPolicies, CleanupRuns, ArchivePolicies and ArchiveExecutions.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls Run where it ran the cleanup subtests, so its
//     runs list the same subtests.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Factory and Run).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 329 (the retention adapters), line 847 (store contract suites per
//     module, run by its adapters) and line 1057 (the metadata store's
//     contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the cleanup and archive cases, in the metadata store's
// contract (pull request 3). Staged in P9d: Factory and Run.
//
// # Testing
//
// The package is the suite. retention/adapters/inmem's TestUnitContract
// and retention/adapters/sql's TestAccContract (SQLite in memory and in a
// file) run Run, and so do the metadata store's TestUnitContract,
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL).
package storetest
