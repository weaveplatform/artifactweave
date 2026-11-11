// Package pub validates Dart pub archives and maintains their native index.
// A package version is one gzip-compressed TAR archive, stored at
// <name>/<name>-<version>.tar.gz, whose pubspec.yaml at the archive root
// must name the package and version its path does. The item tree,
// including a remote repository's cache tree, remains the source of truth:
// the index is rebuilt from it.
//
// # Role
//
// artifacts module, ecosystem layer: the Dart pub archive validation and
// index of the pub slice. Its Handler plugs into the item service through
// the format hooks (decision 0032), so a version published with dart pub
// publish and one written through the item routes meet the same checks;
// the slice's http package (artifacts/ecosystem/pub/http) serves the hosted
// pub protocol over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for pub repositories to Handler, and the work
//     queue, which runs every format's reindex through it (JFrog's
//     /api/pub/{repoKey}/reindex schedules one); and the protocol, which
//     checks names and versions (ValidName, ValidVersion), builds archive
//     paths (Path), reads uploaded and upstream archives (ReadArchive) and
//     decodes the version metadata the index holds (Metadata).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     archives, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex). The generic blackout and
//     include and exclude checks (artifacts/ecosystem/generic) run before
//     the format's own. YAML (gopkg.in/yaml.v3) decodes the pubspecs, and
//     golang.org/x/mod/semver checks versions.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read an archive once before its transaction
//     (Inspect), admit only an archive at a canonical path whose pubspec
//     agrees with it (Admit), refuse a second write of a published version
//     (GuardWrite), index a version and set its properties once its archive
//     is stored (Deployed) and rebuild the index from the stored archives
//     (Reindex). Deletes need no hook: the item service prunes a version
//     whose archive is gone.
//   - The package index: a package per pub package name, with a version per
//     stored archive, whose metadata is the archive's Metadata as JSON: its
//     pubspec, path and SHA-256.
//   - The pub.name, pub.version and pub.sha256 item properties on each
//     archive.
//   - The path and archive rules: ValidName, ValidVersion, Path, ParsePath,
//     ReadArchive, Metadata, ErrArchive and the archive bounds.
//
// # Invariants
//
//   - An archive is read whole within fixed bounds (64 MiB compressed,
//     128 MiB unpacked, 4,096 entries and a 1 MiB pubspec.yaml): one gzip
//     member with nothing after it, unique entries that are regular files
//     or folders with no absolute, backslashed or traversing path, and only
//     zeros after the TAR terminator.
//   - A pubspec has no YAML aliases or anchors, nests at most 64 deep, has
//     string keys, and must encode as JSON.
//   - A published version is immutable through dart pub publish and the
//     item routes alike (409); deletes and restores keep their normal
//     lifecycle.
//   - The property names are stored, so they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and the write guard); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose cache tree the index also covers).
//   - docs/testing/pub.md (connecting Dart, the archive rules, reindex and
//     the evidence).
//   - JFrog pub repositories:
//     https://docs.jfrog.com/artifactory/docs/pub-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/pub), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/765 and
//     https://github.com/weaveplatform/artifactweave/issues/1048
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - archive_test.go: whole-tarball validation, invalid manifests, and the
//     path round trip.
//   - handler_test.go: admission against the archive's identity and the
//     repository policy, and stale or invalid content at Deployed and
//     Reindex.
package pub
