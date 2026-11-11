// Package item writes and removes artifact items: deploy, checksum deploy,
// folders, client checksums and sidecars, delete into the trash can, copy,
// move, archive explode, the remote cache and zap, and the derived work the
// formats queue.
//
// # Role
//
// artifacts module, core layer. The deploy flow of decision 0004 and every
// other write of an item run here, and every write reaches the repository's
// format through its hooks (decision 0032). The target layout publishes this
// service to other modules as artifacts/api.Items, which P9d does not create:
// no other module calls it today, and artifacts/api's Status names it. As
// artifacts core, it never imports the module's adapters or an ecosystem http
// package (decision 0044, dependency rule 8); of the format slices it imports
// only the generic one, the handler a nil format registry falls back to.
//
// # Inputs
//
//   - Driving: the artifacts HTTP handlers and the format protocol packages
//     (deploy, checksum deploy, folders, delete, copy and move, explode,
//     sidecars, restore); remote retrieval, which stores upstream content in
//     a remote repository's cache (Cache) and zaps it (Zap); the build,
//     registry and Terraform state services; and the composition root, which
//     registers the formats' derived work and the remote cache cleanup with
//     the work queue (RegisterWork, RegisterCacheMaintenance).
//   - Driven: the metadata store, through Store: what the formats see
//     (format.Store: repositories, items, the package index, the remote
//     cache and transactions), with the blob references, the trash can, the
//     settings and configuration documents, the work queue and the lease
//     table, which the registry and build services' stores embed; the blob
//     store (platform/blob.Store); the format registry (core/format.Registry;
//     nil means the generic handler for every type); the access module's
//     Keys, which derived work signs metadata with; and the work queue that
//     takes derived work (Enqueuer).
//
// # Outputs
//
//   - Items and their blob references, written in one transaction, with the
//     format's hooks run inside it and the package index kept in step
//     (core/pkgindex).
//   - Deleted content moved to the trash can while it is on, and restored
//     from it (Restore); purged content's blob references released.
//   - Copy and move results (TransferResult), explode results, and the
//     remote cache's entries and zap counts.
//   - Derived work queued on the persistent work queue (decision 0033), and
//     its runs: reindexes (Reindex, ReindexPath) and the remote cache
//     cleanup (KindCacheCleanupSchedule, KindCacheCleanup).
//   - Errors the HTTP layer maps onto statuses: ErrChecksumMismatch,
//     ErrBadChecksum, ErrNoBlob, ErrConflict, ErrCollected, ErrBadArchive,
//     ErrOverwriteDenied with OverwriteDeniedError, and SidecarError.
//
// # Invariants
//
//   - Content is written to the blob store before the transaction opens, so
//     no blob write holds the write lock; the item and its blob reference
//     then commit together or not at all.
//   - Every write runs the repository's format hooks inside its transaction,
//     so a format indexes what it deploys and a delete prunes the package
//     index for every format.
//   - Work kind strings and the JSON written to work items are persisted, so
//     they never change; error and refusal texts reach clients and stay
//     byte-identical, Artifactory's where JFrog documents one.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the deploy flow, items and content-addressed blobs).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (every write reaches a format through the item service).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (derived work and cache maintenance kinds).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" and "Dependency rules".
//   - Research 24: docs/research/24-package-format-handlers.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 199 to 200 (core/item), the deploy write path at lines 459 to
//     500, line 1029 (the mapping row) and rule 2 at lines 35 to 36.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     to #p9-remote-repositories, and #p9d-modular-monolith-migration, epic
//     ART (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1010 and
//     https://github.com/weaveplatform/artifactweave/issues/1088
//
// # Status
//
// Implemented P3 to P9c. Moved here in P9d. Roadmap: gate.go (P17), events.go
// (P11).
//
// # Testing
//
//   - item_test.go, format_test.go, admit_test.go and overwrite_test.go:
//     deploys, checksums and the format hooks around every write.
//   - transfer*_test.go, archive_test.go and sidecar_test.go: copy and move,
//     explode and checksum sidecars.
//   - cache_test.go, cache_maintenance_test.go, smart_test.go and
//     restore_test.go: the remote cache, its cleanup, smart remote
//     properties, restore and zap. cache_restart_test.go's TestAcc tests run
//     the cleanup over SQLite across a restart and a concurrent download.
//   - work_test.go: derived work and reindexes; fault_test.go,
//     guard_test.go and the *_absent_test.go files: store failures, write
//     guards and races.
//   - snapshot_parity_test.go (package item_test): the item service's
//     non-unique snapshot rule matches the Maven slice's.
package item
