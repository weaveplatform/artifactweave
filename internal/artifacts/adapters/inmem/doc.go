// Package inmem is the in-memory implementation of the artifacts module's
// stores: repositories, the remote cache, items, the trash can, the
// package index, builds and their search, Git LFS locks, Bower
// registrations and archived items, the blob and replica indexes and the
// cold tier's indexes.
//
// # Role
//
// artifacts module, adapter layer (in-memory): the reference implementation
// of the module's port.Store, over a shared memtx.Store; its remote cache
// and layout fence are the storekit code it shares with the SQL adapter.
// The composite in-memory store in app/adapters/inmem builds one Store over
// its own memtx.Store, delegates every artifacts accessor to it and
// registers its LayoutWatch with the platform's config store. The package
// imports the standard library, the kernel, the platform, the module's core
// and api and storekit only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//   - The watch's Changed: the name of the layout document that the config
//     store's write created, changed or deleted.
//
// # Outputs
//
//   - Store, from New, with Repos, RemoteCache, Items, Trash, Packages,
//     Blobs, Replicas, GitLFSLocks, BowerRegistrations, Builds,
//     ArchivedItems, ColdBlobs and ColdReplicas, LayoutWatch, and memtx's
//     Tx, AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names, so every call joins the transaction that the caller's
//     context carries, whichever module's store began it.
//   - The repository store writes a remote repository's source record in
//     the transaction that writes its configuration, and Delete removes the
//     repository's cache entries, Git LFS locks and Bower registrations in
//     its transaction too, as the SQL adapter's invalidation and its
//     tables' foreign keys do, so a write that fails leaves none of them
//     changed (decisions 0003 and 0007).
//   - Repository passwords and sensitive header values stay in the
//     configurations as given: sealing at rest is the SQL adapter's, under
//     the master key (decision 0017), and nothing here outlives the process.
//   - ColdBlobs and ColdReplicas are the hot tier's blob and replica index
//     code over the cold_blobs and cold_blob_replicas tables, so both tiers
//     keep the same rules (decision 0024).
//   - The item and build searches match AQL against the stored rows in
//     place and copy only the hits they return.
//
// # References
//
//   - Decision 0003: docs/research/decisions/0003-repository-model.md.
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (the remote cache and its fences).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can and blob garbage collection).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store, layout
//     fencing as a config watch; "Names the migration introduces", which
//     lists Store, New and LayoutWatch).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 610 and 611 (the artifacts in-memory adapter) and line 1056 (the
//     metadata store's in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p5-full-aql-searches-build-info, #p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1061
//   - https://github.com/weaveplatform/artifactweave/issues/1062
//
// # Status
//
// Implemented P2: the repository, item, trash, package and blob stores and
// the archived items and cold indexes of the archive store, as the metadata
// store's (pull request 3), and the replica index with the blob mirror
// (pull request 30). Later phases added the build store (pull request 193),
// the remote cache (pull request 646), layout fencing (pull request 680),
// Git LFS locks (pull request 849) and Bower registrations (pull request
// 912). Moved here in P9d, which added Store, New and LayoutWatch.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the artifacts contract
// (adapters/storetest.Run) on a Store over a fresh memtx.Store. The
// metadata store's in-memory TestUnitContract runs the same cases through
// its own store, with RemoteLayoutFences beside them. docker_page_test.go:
// TestUnitDockerPagesContract runs the Docker page reader's contract
// (adapters/storetest.RunDockerPages), whose order the in-memory reader
// gives as the SQL adapter's indexes do. puppet_page_test.go:
// TestUnitVersionNamesContract runs adapters/storetest.RunVersionNames, the
// contract of the package store's VersionNames (#1404).
package inmem
