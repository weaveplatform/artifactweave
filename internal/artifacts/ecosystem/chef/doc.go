// Package chef validates Chef cookbook archives and maintains their native
// index. A cookbook version is one archive, stored at
// <name>/<name>-<version>.tar.gz, whose one root folder, named for the
// cookbook, holds the metadata.json that names the cookbook, its version,
// its dependencies and its platforms; that manifest, read from the stored
// bytes, is what indexes the version. The item tree remains the source of
// truth.
//
// # Role
//
// artifacts module, ecosystem layer: the Chef cookbook archive validation
// and index of the Chef slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write in a Chef
// repository reaches it whichever API carried it; the slice's http package
// (artifacts/ecosystem/chef/http) serves the Supermarket protocol over what
// it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for chef repositories to Handler, and the work
//     queue, which runs every format's reindex through it; the protocol,
//     which checks names, versions and paths (ValidName, ValidVersion,
//     Path), reads a published or fetched archive (ReadArchive,
//     MaxArchiveBytes), checks an upstream document's keys (UniqueFields)
//     and manifest (ValidateManifest), and decodes the version metadata the
//     index holds (Metadata); and the server's root file chef_item.go,
//     which keeps the generic item routes from serving a remote's cached
//     Supermarket metadata (Serves, IsMetadata).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the archives, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex). The generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds, is the first
//     check Admit applies.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read an archive once before its transaction
//     (Inspect), admit only a valid archive whose identity matches its path
//     (Admit), index a version and set its properties once its archive is
//     stored (Deployed), rebuild the index from the stored archives
//     (Reindex), and keep a remote's cached metadata from leaving its
//     repository by copy or move (GuardTransferSource). Deletes need no
//     hook: the item service prunes a version whose archive is gone.
//   - The package index: a package per cookbook name, with a version per
//     stored archive, whose metadata is the version's Metadata as JSON and
//     whose file is the archive.
//   - The chef.name, chef.version and chef.sha256 item properties
//     (PropName, PropVersion, PropSHA256) on each archive.
//   - The rules the protocol shares: Manifest, Metadata, ValidateManifest,
//     ReadArchive, UniqueFields, ValidName, ValidVersion, Path, ParsePath,
//     IsMetadata, ErrArchive and the archive bounds.
//
// # Invariants
//
//   - An archive is read to its end, gzip checksum included, within fixed
//     bounds (64 MiB compressed, 128 MiB unpacked, a 1 MiB metadata.json and
//     4,096 entries) before its manifest is trusted. Its entries are regular
//     files and folders under one root named for the cookbook, with no link
//     and no duplicate, absolute or traversing path, and nothing but zero
//     padding follows the TAR terminator.
//   - A manifest is one JSON object with no repeated or case-aliased key,
//     its dependencies and platforms included. Its version has two or three
//     numeric parts, and it declares at most 256 dependencies and 256
//     platforms, each with a bounded constraint; a cookbook never depends on
//     itself.
//   - A remote's cached Supermarket metadata (api/v1/...) is admitted as the
//     protocol stored it and never leaves its repository by copy or move
//     (403); any other write must be a cookbook archive in a local or
//     federated repository, or one a remote caches.
//   - The index and the properties are derived from the stored archives,
//     never from a client's claim. The property names are stored, so they
//     stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/chef.md (connecting Knife and knife-art, the protocol and
//     lifecycle, the archive checks and their bounds, and the evidence).
//   - JFrog Chef repositories:
//     https://docs.jfrog.com/artifactory/docs/chef-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/chef), line 385 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/783 and
//     https://github.com/weaveplatform/artifactweave/issues/1045
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - archive_test.go: whole-archive validation, names, paths and
//     constraints, ambiguous manifests, and folders, padding and oversized
//     manifests.
//   - handler_test.go: admission against the archive's identity and the
//     repository class, stale or invalid content at deploy and reindex, and
//     the cached metadata's transfer boundary.
//   - contract_test.go: the format contract suite (formattest), which
//     keeps the index right through deploy, overwrite, delete, copy, move,
//     restore and a remote's cache on every store.
package chef
