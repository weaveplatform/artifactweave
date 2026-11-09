// Package sql is the SQL implementation of the releases module's stores:
// release bundles v1 with their artifacts, keeps, retention audits and
// exports, and the distribution trackers with their per-peer jobs, with the
// module's registration with the SQL layer.
//
// # Role
//
// releases module, adapter layer (SQL). Store implements the module's
// port.Store (port.BundleStore and port.DistributionStore over the shared
// unit of work) over a shared dialect.Store, on SQLite and PostgreSQL
// alike. Module registers the releases stream (adapters/sql/migrations),
// seventh in the canonical module order, with no sealed column. The
// metadata store's SQL store builds one Store over its own dialect store,
// delegates Bundles and Distribution to it and names Module in its module
// list; STO-19 moves that composition into app. The package imports the
// standard library, the kernel, the platform, the module's core and its
// stream only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the releases
//     stream's tables.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Bundles and Distribution, and the dialect
//     store's Tx, AfterCommit and Close.
//   - Module: the name releases and the stream migrations.FS.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence").
//   - The store seals no column, so Module registers none: the keys that
//     sign bundles are in the key pair store (decision 0023), which the
//     access module owns.
//   - Replacing a bundle's artifacts clears and rewrites them in one
//     transaction: a replace that fails leaves the list as it was.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order;
//     "Names the migration introduces", which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 353 (the releases adapters), line 620 (the releases SQL adapter)
//     and lines 631 to 633 (RunModule).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p15-release-bundles-v1-and-distribution and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1059
//
// # Status
//
// Implemented P2: the bundle and distribution stores, as the metadata
// store's (pull request 3). Staged in P9d: Store, New and Module. The
// services that call them arrive with P15.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the releases contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - bundles_test.go: TestAccReleasesClosedDatabase (every call fails on a
//     closed pool), TestAccReleasesCorruptRows (a corrupt document, time or
//     size fails to read) and TestAccReleasesFailingStatements (a keep,
//     export, tracker, job or artifact replace fails when its statement
//     does, and the failed replace leaves the artifacts as they were).
package sql
