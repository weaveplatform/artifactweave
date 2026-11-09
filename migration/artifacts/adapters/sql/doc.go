// Package sql is the SQL implementation of the artifacts module's item,
// trash and package stores, its blob and replica indexes, its archived-item
// store and the cold tier's blob and replica indexes.
//
// # Role
//
// artifacts module, adapter layer (SQL). Store implements, over a shared
// dialect.Store and on SQLite and PostgreSQL alike, the parts of the
// module's port.Store that Items, Trash, Packages, Blobs, Replicas,
// ArchivedItems, ColdBlobs and ColdReplicas return. The repository, remote
// cache, build, Git LFS lock and Bower stores, and with them the module's
// registration with the SQL layer (its stream, sealed columns and hooks),
// are still the metadata store's SQL store's; STO-18 moves them here and
// makes Store a whole port.Store. The metadata store's SQL store builds one
// Store over its own dialect store and delegates the eight accessors to
// it; STO-19 moves that composition into app. The package imports the
// standard library, the kernel, the platform and the module's core and api
// only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the artifacts
//     stream's tables.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Items, Trash, Packages, Blobs, Replicas,
//     ArchivedItems, ColdBlobs and ColdReplicas, and the dialect store's
//     Tx, AfterCommit and Close.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence"), and a
//     caller's writes across stores commit or roll back together.
//   - ColdBlobs and ColdReplicas are the hot tier's blob and replica index
//     code over the cold_blobs and cold_blob_replicas tables, so both tiers
//     keep the same rules (decision 0024).
//   - The replica index compares states with the SQL literals 'present'
//     and 'deleting', which must keep matching mirror.ReplicaPresent and
//     mirror.ReplicaDeleting.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can and blob garbage collection).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect store shared by every module's stores;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 601 to 604 (the artifacts port and SQL adapter), lines 631 to
//     633 (RunModule) and line 1055 (the metadata store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p5-full-aql-searches-build-info and #p9d-modular-monolith-migration,
//     epic STO (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1061
//
// # Status
//
// Implemented P2: the item, trash, package and blob stores and the
// archived items and cold indexes of the archive store, as the metadata
// store's (pull request 3), and the replica index with the blob mirror
// (pull request 30). Staged in P9d: Store and New.
//
// # Testing
//
//   - replicas_test.go: TestAccReplicaDriverFailures (a row that cannot be
//     scanned and a dropped table surface as driver errors, never as store
//     sentinels).
//   - archived_test.go: TestAccArchivedItemsClosedDatabase and
//     TestAccArchivedItemsCorruptRows (every archived-item call fails on a
//     closed pool and on rows corrupted behind the store's back).
//
// They open SQLite over the artifacts stream alone (store_test.go). The
// artifacts contract runs on these stores through the metadata store's
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL) until STO-18 adds the module's own runs.
package sql
