// Package puppet validates Puppet module archives and maintains their
// native index. A module version is one archive, stored at
// <owner>-<module>/<owner>-<module>-<version>.tar.gz, whose one root folder,
// <owner>-<module>-<version>, holds the metadata.json that names the module,
// its version and its dependencies; that manifest, read from the stored
// bytes, is what indexes the version. The item tree remains the source of
// truth.
//
// # Role
//
// artifacts module, ecosystem layer: the Puppet module archive validation
// and index of the Puppet slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write in a Puppet
// repository reaches it whichever API carried it; the slice's http package
// (artifacts/ecosystem/puppet/http) serves the Forge v3 protocol over what
// it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for puppet repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which checks names, versions and paths (ValidName,
//     ValidVersion, Path), checks an upstream release's manifest
//     (ValidateManifest) and a fetched archive (ReadArchive,
//     MaxArchiveBytes) before a remote caches it, and decodes the version
//     metadata the index holds (Metadata).
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
//     stored (Deployed) and rebuild the index from the stored archives
//     (Reindex). Deletes need no hook: the item service prunes a version
//     whose archive is gone.
//   - The package index: a package per module name, with a version per
//     stored archive, whose metadata is the version's Metadata as JSON and
//     whose file is the archive.
//   - The puppet.name, puppet.version and puppet.sha256 item properties
//     (PropName, PropVersion, PropSHA256) on each archive.
//   - The rules the protocol shares: Manifest, Dependency, Metadata,
//     ValidateManifest, ReadArchive, ManifestLinks (a manifest's
//     project_page and issues_url, which a module record answers),
//     ValidName, ValidVersion, Path, ParsePath, ErrArchive and the archive
//     bounds.
//
// # Invariants
//
//   - An archive is read to its end, gzip checksum included, within fixed
//     bounds (64 MiB compressed, 128 MiB unpacked, a 1 MiB metadata.json and
//     4,096 entries) before its manifest is trusted. Its entries are regular
//     files and folders under its one root, with no link and no duplicate,
//     absolute or traversing path, and nothing but zero padding follows the
//     TAR terminator.
//   - A manifest is one JSON object with no repeated or case-aliased key.
//     Its name is <owner>-<module>, its version a full semantic version, and
//     it declares at most 256 dependencies, each on another module, once,
//     with a bounded version requirement.
//   - A remote's cached release metadata (v3/releases/<module>) is admitted
//     as the protocol stored it; any other write must be a module archive in
//     a local or federated repository, or one a remote caches.
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
//   - docs/testing/puppet.md (connecting the Puppet client, the archive
//     checks and their bounds, and the evidence).
//   - JFrog Puppet repositories:
//     https://docs.jfrog.com/artifactory/docs/puppet-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/puppet), line 385 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/784 and
//     https://github.com/weaveplatform/artifactweave/issues/1045
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
//
// # Testing
//
//   - archive_test.go: whole-archive validation, names, paths and version
//     requirements, ambiguous manifests, and folders, padding and oversized
//     manifests.
//   - links_test.go: the project_page and issues_url a manifest gives,
//     read only as strings under exactly those keys.
//   - handler_test.go: admission against the archive's identity and the
//     repository class, and stale or invalid content at deploy and reindex.
//   - contract_test.go: the format contract suite (formattest), which
//     keeps the index right through deploy, overwrite, delete, copy, move,
//     restore and a remote's cache on every store.
package puppet
