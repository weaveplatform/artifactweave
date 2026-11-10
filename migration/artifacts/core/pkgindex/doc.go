// Package pkgindex maintains the package index of decision 0018 for the
// format handlers: which package and version each file belongs to.
//
// # Role
//
// artifacts module, core layer: the package index the format hooks
// maintain. A format's hooks and its reindex write the index through this
// package, and the item service keeps it in step when a delete or move takes
// a version's files. As artifacts core, it never imports the module's
// adapters or an ecosystem http package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the format slices. Their hooks link a written file to its
//     version (Link, Sync) or look its version up (VersionOf), their
//     reindexers rebuild a repository's index from their reading of its tree
//     (Reconcile), and both find the files through Stored. The item service
//     (migration/artifacts/core/item) reads the versions of the items a
//     delete or move takes, and prunes after it (VersionOf, Prune). The
//     Terraform state service and the PyPI and Hugging Face protocol handlers
//     read or link versions directly, and the download path records a
//     version's last download (RecordDownload).
//   - Driven: the metadata store's package index (packages, versions and
//     version files), through Store, which holds only the package store
//     (artifacts/core/port). A format passes its Env.Store, and the item
//     service its own store; both satisfy it.
//   - A Version (repository, package type, package and version), item IDs
//     and times; a repository's configuration (Stored, Reconcile); and a
//     format's reading of a repository's whole tree (Indexed and
//     IndexedVersion).
//
// # Outputs
//
//   - Store, the one-accessor store the functions take.
//   - Index rows written through the store. Link creates the package and
//     version when they are new; Prune deletes the versions left with no
//     files, and a package left with no versions; Sync makes one version
//     what the tree says; Reconcile does so for a whole repository.
//   - VersionOf: the version an item belongs to, with ok false when it
//     belongs to none.
//   - RecordDownload: the version's last download, when the item belongs to
//     a version.
//   - Stored: the repository reference (aql.RepoRef) that a format's hooks
//     and its reindex read: a remote repository's cache, or the repository's
//     own items.
//   - Errors wrap the store's, prefixed "pkgindex: ".
//
// # Invariants
//
//   - The item tree stays the source of truth; the index is derived from it.
//     A reindex rebuilds the index from the tree, and on an index the hooks
//     kept right Reconcile changes nothing.
//   - Hooks and reindex read the same items (Stored), so a reindex rebuilds
//     what the hooks kept. A remote repository's index covers its cache,
//     which alone holds a remote's items.
//   - Reconcile keeps what the tree still backs, with its download statistics
//     and its package's date, and works in name order so that the index
//     changes the same way on every store. It runs inside the caller's
//     transaction.
//   - Sync recreates a version that links a file the tree no longer gives it,
//     with its dates, download statistics and metadata, because the store
//     cannot unlink one file.
//
// # References
//
//   - Decision 0018: docs/research/decisions/0018-package-index.md (the
//     index, derived from the item tree; a remote repository's index over
//     its cache; download statistics).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the hooks and reindexers that maintain the index).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 201 (core/pkgindex), line 1033 (the mapping row) and rule 2 at
//     lines 35 to 36.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (task 4, the package-index skeleton), #p7-npm-go-maven-locals and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/973 and
//     https://github.com/weaveplatform/artifactweave/issues/1087
//
// # Status
//
// Implemented P3 and P7. P3 added Version, Link, VersionOf and Prune, the
// package-index skeleton
// (https://github.com/weaveplatform/artifactweave/issues/45). P7, format
// framework v2, added Indexed, Sync, Reconcile and RecordDownload with the
// format contract suite
// (https://github.com/weaveplatform/artifactweave/issues/268), and Stored,
// for the index of a remote repository's cache
// (https://github.com/weaveplatform/artifactweave/issues/361). Staged in
// P9d, which narrowed the functions' store to Store
// (https://github.com/weaveplatform/artifactweave/issues/1087).
//
// # Testing
//
//   - TestUnitLinkAndUnlink: Link creates the package and its versions, and
//     Prune removes the versions left without files, and the package with
//     its last version.
//   - TestUnitReconcile: a reconcile of what is indexed changes nothing; a
//     version follows the tree while its package keeps its date; what the
//     tree no longer backs goes; and RecordDownload records nothing for a
//     file in no version.
//   - TestUnitReconcilePages: a repository with more packages than one page
//     is reconciled whole.
//   - TestUnitStoreFailures and TestUnitReconcileFaults: every store failure
//     surfaces, from whichever step meets it.
//   - The format contract suite (artifacts/core/formattest) holds every
//     format's index, which this package writes, to the item tree.
package pkgindex
