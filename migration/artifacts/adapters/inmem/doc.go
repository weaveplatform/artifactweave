// Package inmem is the in-memory implementation of the artifacts module's
// item, trash, package and build stores, its blob and replica indexes, its
// archived-item store and the cold tier's blob and replica indexes.
//
// # Role
//
// artifacts module, adapter layer (in-memory): the reference implementation
// of the parts of the module's port.Store that Items, Trash, Packages,
// Blobs, Replicas, Builds, ArchivedItems, ColdBlobs and ColdReplicas
// return, over a shared memtx.Store. The repository, remote cache, Git LFS
// lock and Bower stores are still the metadata store's in-memory store's;
// STO-18 moves them here and makes Store a whole port.Store. The build
// store is here already because the in-memory item search joins build rows
// in place. The metadata store's in-memory store builds one Store over its
// own memtx.Store and delegates the nine accessors to it; STO-19 moves that
// composition into app. The package imports the standard library, the
// kernel, the platform and the module's core only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Items, Trash, Packages, Blobs, Replicas,
//     Builds, ArchivedItems, ColdBlobs and ColdReplicas, and memtx's Tx,
//     AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names, so every call joins the transaction that the caller's
//     context carries, whichever module's store began it.
//   - ColdBlobs and ColdReplicas are the hot tier's blob and replica index
//     code over the cold_blobs and cold_blob_replicas tables, so both tiers
//     keep the same rules (decision 0024).
//   - The item and build searches match AQL against the stored rows in
//     place and copy only the hits they return.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can and blob garbage collection).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 609 and 610 (the artifacts in-memory adapter) and line 1056 (the
//     metadata store's in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p5-full-aql-searches-build-info and #p9d-modular-monolith-migration,
//     epic STO (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1061
//
// # Status
//
// Implemented P2: the item, trash, package and blob stores and the
// archived items and cold indexes of the archive store, as the metadata
// store's (pull request 3), the replica index with the blob mirror (pull
// request 30) and the build store (pull request 193). Staged in P9d: Store
// and New.
//
// # Testing
//
// The package has no tests of its own yet. The artifacts contract runs on
// these stores through the metadata store's in-memory TestUnitContract
// until STO-18 adds the module's own run.
package inmem
