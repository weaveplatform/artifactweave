// Package swift implements the Swift package registry's stored archive
// format. A release is one immutable source archive, a ZIP stored at
// <scope>/<name>/<name>-<version>.zip, the layout JFrog documents for local
// Swift repositories, with exactly one package root holding Package.swift.
// The package index links each package (<scope>.<name>) to the releases
// whose archives are stored, and is rebuilt from them.
//
// # Role
//
// artifacts module, ecosystem layer: the Swift package registry archive
// layout of the Swift slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write, an item PUT,
// copy or restore included, meets the same checks; the slice's http package
// (artifacts/ecosystem/swift/http) serves the Swift Package Registry API
// over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for swift repositories to Handler, and the work
//     queue, which runs every format's reindex through it (JFrog's
//     /api/swift/{repoKey}/reindex schedules one); and the protocol, which
//     checks identities (Package, ValidScope, ValidName, ValidVersion),
//     builds archive paths (Package.ArchivePath) and inspects published,
//     stored and upstream archives (InspectArchive).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     archives, and its store holds the items and the package index
//     (artifacts/core/pkgindex), which Reindex rebuilds from an AQL search
//     of the stored files. The generic blackout and include and exclude
//     checks (artifacts/ecosystem/generic) run before the format's own.
//     InspectArchive spools an archive to a temporary file, since a ZIP is
//     read from its central directory at the end.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that inspect an archive once before its
//     transaction (Inspect), admit only a valid archive at a canonical path
//     that does not replace a release with other content (Admit), link a
//     stored release to its package (Deployed), class every path as unique
//     content (Classify) and rebuild the index from the stored archives
//     (Reindex). Deletes need no hook.
//   - The package index: a package per <scope>.<name>, with a version per
//     stored release archive.
//   - The identity and archive rules: Package with ID, ArchivePath and
//     IsValid, ValidScope, ValidName, ValidVersion, ParseArchivePath,
//     InspectArchive, Archive and MaxArchiveBytes.
//
// # Invariants
//
//   - An archive is read whole within fixed bounds (512 MiB compressed,
//     1 GiB expanded, 100,000 entries and 1 MiB for each manifest): one
//     package root, at the top or in one folder; unique members under it
//     that are regular files or folders, neither encrypted nor at an
//     absolute, backslashed or traversing path; every member read to its
//     end; and manifests that import PackageDescription and declare a
//     swift-tools-version.
//   - A release is immutable: a write of other content to a stored
//     release's path is refused (409), while the same content passes.
//   - Reindex omits an archive that no longer validates rather than
//     advertising it.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose upstream archives InspectArchive checks).
//   - docs/testing/swift.md (connecting SwiftPM, the native operations, the
//     archive validator and the evidence).
//   - JFrog Swift repositories:
//     https://docs.jfrog.com/artifactory/docs/swift-repositories, and the
//     Swift Package Registry specification:
//     https://github.com/swiftlang/swift-package-manager/blob/main/Documentation/PackageRegistry/Registry.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/swift), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/766 and
//     https://github.com/weaveplatform/artifactweave/issues/1048
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - path_test.go: the identity grammar and the archive path.
//   - archive_test.go: archive inspection and a failing read.
//   - coverage_test.go: unsafe members and cancelled inspection, the bounds
//     and every member's checks, admission of corrupt and changed archives,
//     and a reindex that restores only valid stored archives.
package swift
