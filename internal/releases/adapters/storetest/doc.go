// Package storetest is the contract suite of the releases module's stores:
// one set of expectations that every implementation of the bundle and
// distribution stores must meet.
//
// # Role
//
// releases module, adapter layer, test support. The module's SQL and
// in-memory adapters run Run with a Factory that returns a fresh store; the
// metadata store's contract runs it too, so its in-memory, SQLite and
// PostgreSQL stores pass the same cases. The package imports the standard
// library, the kernel, the platform and the module's core only.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty port.Store and registers
//     its cleanup with t.
//
// # Outputs
//
//   - Factory and Run.
//   - The subtests, one per concern, each on a store of its own: Bundles,
//     BundleRetention, BundleExports and Distribution.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls Run where it ran them, so its runs list the same
//     subtests.
//   - The fixtures' checksums are those of the metadata store's contract
//     fixtures, which this package cannot import, so the cases store the
//     same values they did there.
//
// # References
//
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Factory and Run).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 353 (the releases adapters), line 847 (store contract suites per
//     module, run by its adapters) and line 1057 (the metadata store's
//     contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1059
//
// # Status
//
// Implemented P2: the bundle and distribution cases, in the metadata
// store's contract (pull request 3). Moved here in P9d, which added Factory
// and Run.
//
// # Testing
//
// The package is the suite. releases/adapters/inmem's TestUnitContract and
// releases/adapters/sql's TestAccContract (SQLite in memory and in a file)
// run Run, and so do the metadata store's TestUnitContract,
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL).
package storetest
