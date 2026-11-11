// Package bazelmodules validates Bazel module registry uploads. A hosted
// version is modules/<name>/<version>/MODULE.bazel with one source
// archive (.tar.gz, .tgz, .tar or .zip) beside it: the MODULE.bazel must
// declare that name and version literally, and the archive must be a safe
// archive of the type its name gives. A registry's metadata.json and
// source.json are never uploaded; the slice's protocol derives them from
// the stored files on each request.
//
// # Role
//
// artifacts module, ecosystem layer: the Bazel module registry upload
// validation of the Bazel modules slice. Its Handler plugs into the item
// service through the format hooks (decision 0032); the slice's http
// package (artifacts/ecosystem/bazelmodules/http) serves the Bazel index
// registry protocol over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for bazelmodules repositories to Handler, and
//     the work queue, which runs every format's reindex through it; and the
//     protocol, which parses paths and modules (ValidName, ValidVersion,
//     ParseFile, ParseModule) and builds a version's source.json
//     (Integrity, ArchiveStripPrefix).
//   - Driven: the format hooks' environment (format.Env), whose blobs hold
//     the uploads and through whose store Handler lists a version's
//     archives and a repository's publications; the archive scanner
//     (platform/archive), which reads an archive within bounds; and the
//     generic deploy policy (artifacts/ecosystem/generic), which Admit
//     applies after the Bazel checks.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that check an upload before its transaction
//     (Inspect), admit only a valid publication (Admit), refuse a second
//     archive for one version (GuardWrite) and re-check every publication
//     (Reindex).
//   - The rules the protocol shares: File, ParseFile, ParseModule,
//     ValidName, ValidVersion, Serves, Integrity and ArchiveStripPrefix.
//
// # Invariants
//
//   - A module's identity comes from the literal name and version of its
//     single module() call, never from running Starlark, and must match
//     its path.
//   - A version has at most one archive, since its source.json can name
//     only one.
//   - An archive's content has the type its name gives, and holds only
//     regular files and folders with clean paths, within 10,000 entries
//     and 512 MiB.
//   - Nothing is derived into storage: the registry view is computed from
//     the current stored files, so no second copy of the metadata can go
//     stale.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/bazel-modules.md (configuring Bazel, publishing a
//     module, and remote and virtual repositories).
//   - JFrog Bazel modules repositories:
//     https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/bazelmodules), line 379 (the slice row) and line
//     997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/772 and
//     https://github.com/weaveplatform/artifactweave/issues/1040
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Moved here in P9d.
//
// # Testing
//
//   - bazelmodules_test.go: paths and module identity, the integrity
//     encoding, the strip prefix of real archive layouts, and admission of
//     invalid paths.
//
// The hooks are also tested end to end through the protocol, whose
// package runs the pinned Bazel client (TestAccBazelClient).
package bazelmodules
