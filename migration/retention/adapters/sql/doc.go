// Package sql is the SQL implementation of the retention module's stores:
// cleanup policies and runs, and archive policies and executions, with the
// module's registration with the SQL layer.
//
// # Role
//
// retention module, adapter layer (SQL). Store implements the module's
// port.Store (port.CleanupStore and port.ArchiveStore over the shared unit
// of work) over a shared dialect.Store, on SQLite and PostgreSQL alike.
// Module registers the retention stream (adapters/sql/migrations), fifth in
// the canonical module order. The metadata store's SQL store builds one
// Store over its own dialect store, delegates Cleanup and Archive to it and
// names Module in its module list; STO-19 moves that composition into app.
// The archived items and the cold indexes that archive executions act on
// are artifacts' records and stay with the artifacts stores. The package
// imports the standard library, the kernel, the platform, the module's
// core and api and its stream only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the retention
//     stream's tables.
//   - The ports' calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Cleanup and Archive, and the dialect store's Tx,
//     AfterCommit and Close.
//   - Module: the name retention and the stream migrations.FS; the module
//     seals no column.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence").
//   - No retention table holds a secret, so Module registers no sealed
//     column and key rotation passes the module by (decision 0017).
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md
//     (archive policies and executions are retention's records).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order;
//     "Names the migration introduces", which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 329 (the retention adapters), line 618 (the retention SQL
//     adapter) and lines 631 to 633 (RunModule).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the cleanup and archive stores, as the metadata store's
// (pull request 3). Staged in P9d: Store, New and Module. The services that
// call them arrive with P11 (cleanup) and P16 (the cold-storage tier).
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the retention contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - cleanup_test.go: TestAccCleanupUnmigrated (every cleanup call reports
//     the driver's error on a database without the schema).
//   - archive_test.go: TestAccArchiveClosedDatabase (every archive call
//     fails on a closed pool) and TestAccArchiveCorruptRows (a policy or an
//     execution whose row holds text where a number or a boolean belongs
//     fails to read).
package sql
