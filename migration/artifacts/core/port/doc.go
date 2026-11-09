// Package port declares the artifacts module's store ports: repositories,
// items, the blob and replica indexes, the trash can, the package index,
// builds, the remote cache, Git LFS locks, Bower registrations and archived
// items, the values their calls take and return, the checks every store
// applies before it writes, and Store, every store the module owns.
//
// # Role
//
// artifacts module, core layer, driven store ports. The metadata store's
// SQL and in-memory stores implement them, and both satisfy Store; STO-17
// and STO-18 move those implementations into the module's adapters. The
// ports take and return the module's core/model values, repository
// configurations (artifacts/api/repo), AQL queries and hits (core/aql),
// kernel.Page, the platform's blob row and the mirror's replica rows. The
// metadata store aliases every name here but Store (its
// bridge_artifacts_port.go) until PRO-02 deletes the bridge.
//
// # Inputs
//
//   - Repository configurations, items with their properties and
//     statistics, blob and replica rows, trash entries, packages and
//     versions, build runs with their promotions and retention, remote
//     cache keys, tokens and entries, Git LFS locks, Bower registrations
//     and archived items, to store; keys, paths, ids and checksums to look
//     up.
//   - A RepoFilter, GitLFSLockFilter, ArchivedFilter or the mirror's
//     ReplicaFilter, with a kernel.Page, to narrow a listing; an AQL query
//     with its options to search items or builds.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - RepoStore with RepoFilter; ItemStore with Download, DeleteResult and
//     RepoUsage; BlobIndex with the sentinel ErrDeleting; ReplicaIndex;
//     TrashStore; and PackageStore.
//   - BuildStore with NormaliseBuild; BowerRegistrationStore;
//     GitLFSLockStore with GitLFSLockFilter; and ArchivedItemStore with
//     ArchivedFilter.
//   - CleanProperties, CheckPropertyKeys and CheckBuildPropertyKeys, how
//     every store keeps and checks properties.
//   - RemoteCacheStore, with NewRemoteSource, RemoteConfigChanged,
//     RemoteSourceConfig, RemotePolicyChanged, RemoteCacheID,
//     ValidateRemoteKey, RemotePathMatches and ValidateRemoteEntry.
//   - Store: tx.Runner and the 13 accessors Repos, RemoteCache, Items,
//     Blobs, Replicas, Trash, Packages, GitLFSLocks, BowerRegistrations,
//     Builds, ArchivedItems, ColdBlobs and ColdReplicas.
//
// # Invariants
//
//   - Every store joins the caller's unit of work: a call made with a
//     context inside Tx is part of that transaction, and is rolled back
//     with it.
//   - ColdBlobs and ColdReplicas index the cold tier (the cold_blobs and
//     cold_blob_replicas tables) with the same code as Blobs and Replicas,
//     so they return the same port types (decision 0024).
//   - BlobIndex and ReplicaIndex satisfy the mirror's own index ports,
//     because they take and return the mirror's replica rows and the
//     platform's blob row rather than copies of them (decision 0028).
//   - Errors are the kernel's sentinels, wrapped, and ErrDeleting for a
//     blob that garbage collection is removing: a deleting row refuses
//     references, so no deploy references content that is about to go
//     (decision 0010).
//   - Every store checks property keys before it writes and keeps values
//     as CleanProperties returns them, so a property write stores every
//     key or none, whichever route it came by.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (the remote cache and its fences).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can and blob garbage collection).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence", "Type-alias bridges" and "Names the migration
//     introduces", which lists Store).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 189 to 192 (the artifacts module's core/port/store.go), lines
//     577 and 578 (each module owns its store ports), line 601 (the
//     artifacts port) and line 1055 (the metadata store split into each
//     module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p5-full-aql-searches-build-info, #p9-remote-repositories,
//     #p9b-newly-identified-artifactory-formats and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1055
//
// # Status
//
// Implemented P2: RepoStore, ItemStore, BlobIndex, TrashStore and
// PackageStore, with the metadata store's in-memory, SQLite and PostgreSQL
// stores (pull request 3), and ReplicaIndex with the blob mirror (pull
// request 30). Later phases added BuildStore (pull request 193), the
// property key checks (pull request 348), RemoteCacheStore (pull request
// 646), GitLFSLockStore (pull request 849) and BowerRegistrationStore (pull
// request 912). Staged in P9d, which declares ArchivedItemStore and Store.
//
// # Testing
//
// The package has no tests of its own. The metadata store's in-memory and
// SQL stores are checked against Store at compile time beside the other
// modules' ports (pkg/store/inmem/inmem_test.go and
// internal/sqlstore/sqlcommon/store_test.go), and its contract subtests run
// every port on its in-memory, SQLite and PostgreSQL stores.
package port
