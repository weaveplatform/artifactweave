// Package storetest is the contract suite of the artifacts module's
// stores: one set of expectations that every implementation of the item,
// trash, package, build, remote cache, Git LFS lock, Bower registration and
// archived-item stores and of the blob and replica indexes must meet.
//
// # Role
//
// artifacts module, adapter layer, test support. The metadata store's
// contract runs Run with a Factory over its own stores, so its in-memory,
// SQLite and PostgreSQL stores pass the same cases; STO-18 adds the
// module's own runs once its adapters implement the whole port.Store. The
// package imports the standard library, the kernel, the platform and the
// module's core and api only.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty port.Store and registers
//     its cleanup with t.
//
// # Outputs
//
//   - Factory and Run.
//   - The subtests, one per concern, each on a store of its own: Blobs,
//     ColdBlobs, BlobDeletion, ColdBlobDeletion, BlobTiersSeparate,
//     Replicas, ReplicaTiersSeparate, Repos, Items, ItemCreate, ColonPaths,
//     ItemProperties, RepositoryRoot, ItemStats, ItemSearch,
//     LongPropertyValues, LongPropertyKeys, Trash, Packages, GitLFSLocks,
//     BowerRegistrations, RemoteCache, RemoteCacheFences, RemoteCacheAtomic,
//     RemoteCacheEvict, RemoteCacheConcurrent, RemoteCacheUsage,
//     RemoteSmartState, ArchivedItems, Builds, BuildRuns, BuildDeletes,
//     BuildRename, BuildPromotions, BuildFiles and BuildRetention.
//   - LongText, the random text the long property cases store, which the
//     server's long-property test shares.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls Run where it ran Blobs, so its runs list the
//     same subtests.
//   - The subtests use only the artifacts accessors. RemoteLayoutFences is
//     not one of them: a layout document lives in the platform's config
//     store, so the metadata store's contract registers it beside Run.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (the remote cache and its fences).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Factory and Run).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 611 and 612 (the artifacts storetest adapter), line 847 (store
//     contract suites per module, run by its adapters) and line 1057 (the
//     metadata store's contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1061
//
// # Status
//
// Implemented P2: the content, blob and archived-item cases, in the
// metadata store's contract (pull request 3). Later phases added the
// replica cases (pull request 30), the build cases (pull request 193), the
// property key cases (pull request 348), the remote cache cases (pull
// request 646), the Git LFS lock cases (pull request 849) and the Bower
// registration cases (pull request 912). Staged in P9d: Factory and Run.
//
// # Testing
//
// The package is the suite. The metadata store's TestUnitContract,
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL) run Run.
package storetest
