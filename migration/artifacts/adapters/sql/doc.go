// Package sql is the SQL implementation of the artifacts module's stores:
// repositories with their sealed secrets, the remote cache, items, the
// trash can, the package index, builds and their search, Git LFS locks,
// Bower registrations and archived items, the blob and replica indexes and
// the cold tier's indexes, with the module's registration with the SQL
// layer.
//
// # Role
//
// artifacts module, adapter layer (SQL). Store implements the module's
// port.Store over a shared dialect.Store, on SQLite and PostgreSQL alike;
// its remote cache and layout fence are the storekit code it shares with
// the in-memory adapter. Module registers the artifacts stream
// (adapters/sql/migrations), its two sealed columns and the repository
// header backfill, second in the canonical module order. The metadata
// store's SQL store builds one Store over its own dialect store, delegates
// every artifacts accessor to it, registers its LayoutWatch with the
// platform's config store and names Module in its module list; STO-19
// moves that composition into app. The package imports the standard
// library, the kernel, the platform, the module's core and api, storekit
// and its stream only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the artifacts
//     stream's tables, with the keyring that seals repository passwords and
//     sensitive header values, or none.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//   - The watch's Changed: the name of the layout document that the config
//     store's write created, changed or deleted.
//
// # Outputs
//
//   - Store, from New, with Repos, RemoteCache, Items, Trash, Packages,
//     Blobs, Replicas, GitLFSLocks, BowerRegistrations, Builds,
//     ArchivedItems, ColdBlobs and ColdReplicas, LayoutWatch, and the
//     dialect store's Tx, AfterCommit and Close.
//   - Module: the name artifacts, the stream migrations.FS, the two sealed
//     columns and the BeforeReseal and AfterMigrate hooks.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence"), and a
//     caller's writes across stores commit or roll back together.
//   - The repository store writes a configuration's sealed password and
//     header values, and a remote repository's source record, in the
//     transaction that writes the repository row, and Delete invalidates
//     the repository's cache entries in its transaction too, so a write
//     that fails leaves none of them changed (decisions 0003 and 0007).
//   - Every sealed column and both hooks are in Module:
//     repository_secrets.password with the purpose repositories.password
//     under the repository key, and repository_header_secrets.value with
//     the purpose repositories.http_header under the row id of the
//     repository, header position and name, so key rotation re-seals them
//     (decision 0017). Neither the purpose nor the row id ever changes: both
//     are authenticated with every value.
//   - The header backfill moves sensitive header values still held in a
//     repository document into sealed rows. It runs as AfterMigrate at every
//     open, with the configured keyring, because migrations never see key
//     material, and as BeforeReseal ahead of every re-seal; the metadata
//     store's open runs it before the access module's identity-search
//     backfill.
//   - ColdBlobs and ColdReplicas are the hot tier's blob and replica index
//     code over the cold_blobs and cold_blob_replicas tables, so both tiers
//     keep the same rules (decision 0024).
//   - The replica index compares states with the SQL literals 'present'
//     and 'deleting', which must keep matching mirror.ReplicaPresent and
//     mirror.ReplicaDeleting.
//   - The SQL text, the table names, the purpose strings, the row ids and
//     the error texts are those the metadata store's SQL store used before
//     the module owned them.
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
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect store shared by every module's stores, one
//     dialect.Module per module in the canonical order, backfills as hooks,
//     layout fencing as a config watch; "Names the migration introduces",
//     which lists Store, New, LayoutWatch and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 601 to 609 (the artifacts port, SQL adapter and stream), lines
//     586 and 587 (the header and remote cache suites, which the module's
//     tests and the metadata store's split by owner), lines 631 to 633
//     (RunModule) and line 1055 (the metadata store split per owner).
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
// (pull request 30). Later phases added the build store (pull request 193)
// and build search (pull request 206), the remote cache (pull request 646),
// sealed header values (pull request 656), layout fencing (pull request
// 680), Git LFS locks (pull request 849) and Bower registrations (pull
// request 912). Staged in P9d: Store, New, LayoutWatch and Module.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the artifacts contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL, with RemoteLayoutFences beside them.
//   - repository_headers_suite_test.go: TestAccRepositoryHeaderSecrets
//     (sensitive header values are sealed in rows of their own and survive
//     the configuration merge) and TestAccRepositoryHeaderMigration
//     (AfterMigrate moves a legacy document's header values into sealed
//     rows, keeps its other fields, changes nothing on a later open and
//     leaves a re-seal nothing to do), on SQLite in memory and in a file and
//     on PostgreSQL.
//   - remotecache_suite_test.go: TestAccRemoteCachePersistence (each
//     repository keeps a source lifetime of its own, and the lifetime and
//     the completed and in-flight retrievals survive a reopen), on the same
//     three backends.
//   - repository_headers_test.go: five TestAccRepositoryHeader tests (a
//     failed header write rolls back, corrupt header rows fail closed, a
//     failed backfill leaves no partial secrets and succeeds on retry, a
//     re-seal seals plain values and refuses a wrong key, and the backfill
//     refuses a keyring that cannot open the existing secrets).
//   - remotecache_test.go: TestAccRemoteCacheDatabaseFaults (a failed or
//     corrupt statement anywhere in a cache call fails the call) and
//     TestAccRemoteCacheClosedAndBadPage.
//   - builds_test.go: TestAccBuildsFaults and TestAccBuildsUnmigrated (every
//     build call reports an injected fault, a corrupt document or a missing
//     schema as the driver's error, never as a store sentinel).
//   - content_test.go: TestAccContentWriteFailures,
//     TestAccContentPropertyAndStatsFailures, TestAccContentVersionFailures
//     and TestAccContentUnreadableRows (failed repository, item, property and
//     package version writes, undecodable documents and passwords read
//     without the key that sealed them).
//   - restart_test.go: TestAccP9bPackageIndexesSurviveRestart,
//     TestAccGitLFSLocksSurviveRestart and
//     TestAccBowerRegistrationsSurviveRestart (rows written to a SQLite file
//     are read back after it is closed and opened again, and deletes stay
//     deleted).
//   - replicas_test.go: TestAccReplicaDriverFailures (a row that cannot be
//     scanned and a dropped table surface as driver errors, never as store
//     sentinels).
//   - archived_test.go: TestAccArchivedItemsClosedDatabase and
//     TestAccArchivedItemsCorruptRows (every archived-item call fails on a
//     closed pool and on rows corrupted behind the store's back).
//
// They open SQLite over the artifacts registration alone (store_test.go),
// which migrates the stream and runs the header backfill as the metadata
// store's open does.
package sql
