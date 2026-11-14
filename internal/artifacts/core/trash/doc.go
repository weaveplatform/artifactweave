// Package trash restores, cleans and purges the trash can (decision 0010).
// Deleting an item moves its record here (internal/artifacts/core/item), as
// does deleting a repository (Keep, internal/artifacts/core/repos).
//
// # Role
//
// artifacts module, core layer: restore, clean and purge of the trash can,
// and the tree the HTTP layer shows as Artifactory's auto-trashcan
// repository. Restored items are recreated through the item service. As
// artifacts core, it never imports the module's adapters or an ecosystem
// http package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the trash can API (POST /api/trash/empty,
//     DELETE /api/trash/clean/{repoKey}/{path} and
//     POST /api/trash/restore/{repoKey}/{path}) and the storage API's views
//     of auto-trashcan paths; the repository delete
//     (internal/artifacts/core/repos), which keeps a repository's content
//     here (Keep); the garbage collector (internal/artifacts/core/gc),
//     which expires entries (PurgeExpired); and the format contract suite
//     (artifacts/core/formattest), which restores.
//   - Driven: the metadata store, through Store, for trash entries, items
//     and their properties, blob references, the trash can settings and
//     transactions (Settings takes the settings store alone);
//     and the item service (internal/artifacts/core/item), which recreates
//     restored items and names the sidecars a file's format keeps with it
//     (format.SidecarKeeper).
//   - The trash can settings: whether it is on, and the retention period in
//     days. Unset, it is on with Artifactory's 14 days.
//   - The caller's permit (Restore) and filter (Children, Below, Walk,
//     Holds), which judge each item at its original path. Nil allows every
//     item, for an administrator.
//
// # Outputs
//
//   - Settings, with Artifactory's defaults filled in.
//   - Entries and Find: what was trashed at or below a path, and the newest
//     entry at one path.
//   - Keep: an entry for every item of a repository's content, and how many
//     references the entries hold per sha256, so that the repository's
//     delete releases only the others.
//   - Restore: how many items it brought back, each path's newest trashed
//     copy with the sidecars its delete removed; ErrNothingTrashed,
//     ErrNotPermitted, or the item service's error on a conflict.
//   - Clean, Empty and PurgeExpired: how many entries each purged, with the
//     blob references their files held released.
//   - Children and Below: the trash can's tree as Artifactory's
//     auto-trashcan repository holds it (Child, Node), sorted by path.
//     Walk yields Below's nodes a store page at a time, from the entries in
//     path order (port.TrashStore.BelowByPath), for the storage views that
//     write the tree as they read it (#1409); Holds says whether the caller
//     sees anything below a folder, reading one entry when the first is
//     visible.
//   - Repository ("auto-trashcan"), DefaultRetentionDays and DefaultBatch.
//
// # Invariants
//
//   - A trashed file keeps its blob reference until a restore takes it or a
//     purge releases it, so it can be restored until its retention period
//     passes. Once a purge releases it, the next garbage collection removes
//     the content when nothing else holds it.
//   - A restore asks the permit about every selected item before any batch
//     moves, so one refusal restores nothing.
//   - Each batch (Service.Batch, DefaultBatch when zero) is one transaction,
//     so emptying a large trash can never holds one long transaction. A
//     conflict stops a restore with the earlier batches kept.
//   - A purge skips an entry that is already gone, whose reference whoever
//     removed it released, so no reference is released twice.
//   - Nothing expires while the trash can is off, as 7.x's small garbage
//     collection does not run then.
//   - Walk yields exactly Below's nodes in Below's order, holding one store
//     page and the answers for the current path's prefixes: before an
//     entry's node it asks whether each prefix the path continues with a
//     byte before "/" is a folder, since such a folder sorts before the
//     entry though what makes it one sorts after.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md,
//     "Trash can" and "Implementation (P3)".
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (SidecarKeeper, whose sidecars a restore brings back with their file).
//   - Research 02: docs/research/02-artifactory-core-api-inventory.md, "Trash
//     can and garbage collection".
//   - https://docs.jfrog.com/artifactory/docs/trash-can and
//     https://docs.jfrog.com/artifactory/reference/restoreitemfromtrashcan.md
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 203 (core/trash), line 1037 (the mapping row) and rule 2 at lines
//     35 to 36.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (task 2 and tracking item 7), #p8-pypi-nuget-helm-terraform-locals and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/973 and
//     https://github.com/weaveplatform/artifactweave/issues/1088
//
// # Status
//
// Implemented P3 and P8. P3 added restore, clean, empty and expiry
// (https://github.com/weaveplatform/artifactweave/issues/48), and its
// follow-ups added the permit on the repository a restore takes items from
// (https://github.com/weaveplatform/artifactweave/issues/380), the filter
// that shows anyone else only what they may see
// (https://github.com/weaveplatform/artifactweave/issues/439), the tree
// behind the storage API's views (Below and Node,
// https://github.com/weaveplatform/artifactweave/issues/455), no expiry
// while the trash can is off
// (https://github.com/weaveplatform/artifactweave/issues/490) and Keep, for
// a deleted repository's content
// (https://github.com/weaveplatform/artifactweave/issues/504). P8 restores
// the sidecars a format keeps with a file
// (https://github.com/weaveplatform/artifactweave/issues/533). Moved here in
// P9d, which narrowed the service's store to Store
// (https://github.com/weaveplatform/artifactweave/issues/1088).
//
// # Testing
//
//   - TestUnitSettings: the defaults, a stored setting, a stored setting with
//     no retention, and a malformed one.
//   - TestUnitRestoreToOriginal, TestUnitRestoreElsewhere,
//     TestUnitRestoreNewestAndOverwrite and
//     TestUnitRestoreIntoExistingFolder: where a restore puts items, and
//     which copy it takes.
//   - TestUnitRestorePermit and TestUnitRestoreConflict: one refusal restores
//     nothing, and a conflict keeps the earlier batches.
//   - TestUnitRestoreTakesSidecars, TestUnitRestoreLeavesOtherDeletesSidecars,
//     TestUnitRestoreSidecarPermit and TestUnitRestoreSidecarFindFails: the
//     sidecars a restore brings back, and those it leaves.
//   - TestUnitKeep and TestUnitKeepPages: a repository's whole content, a
//     page at a time.
//   - TestUnitCleanEmptyPurge and TestUnitPurgeExpiredWhileOff: purges, and
//     expiry with the trash can on and off.
//   - TestUnitChildrenAndFind, TestUnitBelow and TestUnitChildrenFiltered:
//     the tree, and what a filter keeps of it.
//   - TestUnitStoreFailures and TestUnitDefaultClock: every store failure
//     surfaces, and a service with no clock uses the current time.
package trash
