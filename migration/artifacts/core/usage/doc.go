// Package usage computes the storage summary (GET /api/storageinfo): blob
// totals, item totals per repository and for the trash can, and the capacity
// of the volume a local blob store lives on.
//
// # Role
//
// artifacts module, core layer: the storage summary behind
// GET /api/storageinfo. It computes the values, and the HTTP layer renders
// them in 7.x's JSON. As artifacts core, it never imports the module's
// adapters or an ecosystem http package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the storage summary handler of GET /api/storageinfo, after
//     its access check.
//   - Driven: the metadata store, for the blob totals, the item totals of
//     each repository and remote cache, the repositories, and the trash
//     can's entries, a page at a time.
//   - The hot blob store's Location, which the application derives from the
//     blob store configuration and hands to the server: the name Artifactory
//     gives its binary provider chain, the directory it reports, and the
//     local root whose volume reports capacity.
//   - The volume's capacity, from the operating system: statfs on Unix
//     (space_unix.go) and GetDiskFreeSpaceEx on Windows (space_windows.go).
//     Elsewhere it is unknown (space_other.go).
//
// # Outputs
//
//   - Summarize's Summary: the blob count and size; the file count and size
//     before deduplication, and the item count with the folders, the trash
//     can's included; the Location; the volume's Space, nil when unknown;
//     and the Rows, sorted by key. There is one Row per repository, a remote
//     repository's under its cache alias ("<key>-cache", type CACHE), and
//     one for the trash can.
//   - DefaultProject, the project key of a repository in no project and of
//     the trash can's row, and NoFilesystem, the directory an object store
//     reports.
//
// # Invariants
//
//   - The summary is computed on every request rather than cached, so it
//     never lags writes.
//   - The trash can counts as the repository 7.x keeps it in
//     (auto-trashcan), in its row and in every total, as its storage listing
//     shows it: a path trashed twice is one file, of the size last trashed,
//     and the folders above a trashed item, its repository's among them, are
//     folders. A count of trash entries would differ on both.
//   - The capacity of object storage is unknown, as is that of a volume the
//     operating system cannot read; Space is then nil.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md,
//     "Implementation (P3)" (the storage summary, computed on every request,
//     with the trash can in an auto-trashcan row and in the totals).
//   - Research 15: docs/research/15-data-model.md, section 2.2 (7.x's
//     storage summary and the captures that settle its fields).
//   - Research 02: docs/research/02-artifactory-core-api-inventory.md (the
//     storage summary's endpoints and access).
//   - https://docs.jfrog.com/artifactory/reference/getstoragesummaryinfo.md
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 210 (core/usage), line 1038 (the mapping row) and rule 2 at lines
//     35 to 36.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (task 2, storageinfo), #p2b-cloud-blob-storage-and-mirroring and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/973
//
// # Status
//
// Implemented P3, with 7.x's fields from follow-ups filed under P2b. P3
// added Summarize over the blob totals and the item totals per repository,
// and the volume's capacity
// (https://github.com/weaveplatform/artifactweave/issues/48). The
// follow-ups added the trash can's row and totals, project keys and the
// binary provider chain names
// (https://github.com/weaveplatform/artifactweave/issues/390), and
// NoFilesystem for an object store
// (https://github.com/weaveplatform/artifactweave/issues/461). Staged in
// P9d. Roadmap: Summarize takes a narrow store in place of the whole store
// (https://github.com/weaveplatform/artifactweave/issues/1088).
//
// # Testing
//
//   - TestUnitSummarize: the totals; the rows with their types, package
//     types and project keys; the trash can's row, while it is empty and
//     once items are trashed; and the capacity of a local volume, unknown
//     for object storage and for a volume that cannot be read.
//   - TestUnitSummarizeReadsTheWholeTrash: more trash entries than one page
//     of the trash listing holds.
//   - TestUnitSummarizeFailures: every store failure surfaces.
package usage
