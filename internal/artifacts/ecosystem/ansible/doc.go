// Package ansible validates Ansible collection archives and indexes them as
// Galaxy collection versions. A collection is <namespace>.<name>, and its
// archive for a version is stored at
// collections/<namespace>/<name>/<version>/<namespace>-<name>-<version>.tar.gz,
// whose MANIFEST.json must name that identity. Once an archive is stored,
// its identity and SHA-256 are recorded as item properties and its version
// is indexed with its dependencies and description, which the Galaxy API
// answers from.
//
// # Role
//
// artifacts module, ecosystem layer: the Ansible collection archive
// validation and index of the Ansible slice. Its Handler plugs into the
// item service through the format hooks (decision 0032), so every write in
// an Ansible repository reaches it whichever API carried it; the slice's
// http package (artifacts/ecosystem/ansible/http) serves the Galaxy v3 API
// over the index it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for ansible repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and the
//     protocol, which checks identities, versions and dependency ranges
//     (ValidName, ValidVersion and ValidDependencies), builds and parses
//     archive paths (Path and ParsePath) and validates every published or
//     fetched archive (ReadArchive).
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
//     (Inspect), admit only a valid archive at the path its identity gives
//     (Admit), record a stored archive's properties and index its version
//     (Deployed), and read every stored archive again to rebuild the index
//     (Reindex). Deletes need nothing more: an archive is its version's
//     only file, and the item service prunes a version left without files.
//   - The package index: a package per <namespace>.<name>, with a version
//     per collection version, whose metadata is the archive's Metadata as
//     JSON and whose only file is the archive.
//   - The ansible.namespace, ansible.name, ansible.version and
//     ansible.sha256 item properties on each stored archive.
//   - The rules the protocol shares: Metadata, ReadArchive, ValidName,
//     ValidVersion, ValidDependencies, Path, ParsePath, MaxArchiveBytes and
//     ErrArchive.
//
// # Invariants
//
//   - An archive is read within fixed bounds (128 MiB compressed, 512 MiB
//     of file content, 4,096 entries and 1 MiB each for MANIFEST.json and
//     FILES.json; the decompressed stream, tar framing included, is bounded
//     too) without extracting it. Its entries are regular files and folders,
//     with no link, special file or duplicate, and no absolute,
//     non-canonical or traversing path or one holding a backslash or NUL.
//   - MANIFEST.json gives a valid identity and names FILES.json with that
//     file's SHA-256; FILES.json lists every other file of the archive with
//     its SHA-256, and only folders the archive holds. Dependencies are at
//     most 256 <namespace>.<name> keys, each with "*" or comma-separated
//     version terms within 1,024 bytes.
//   - The path gives the identity: an archive is admitted only at the Path
//     of the identity its MANIFEST.json names (400), and a stored archive
//     that no longer validates or matches its path fails a reindex.
//   - A remote repository's cached archives stay out of the index: a remote
//     answers from the upstream Galaxy metadata in the shared source cache.
//   - The items keep the ansible.* property names, so they never change.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks, and its section "P9c Ansible collection adapter
//     (#782)"); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose cached archives the hooks leave out of the index).
//   - docs/operations/ansible.md (the archive path and identity rules, the
//     integrity checks and their limits, and the evidence), and
//     docs/client-recipes/ansible.md (the client recipe).
//   - JFrog Ansible repositories:
//     https://docs.jfrog.com/artifactory/docs/ansible-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/ansible), line 385 (the slice row) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/782 and
//     https://github.com/weaveplatform/artifactweave/issues/1051
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - archive_test.go (external, ansible_test): the client-built fixture
//     (testdata/weave-sample-1.2.3.tar.gz, described in testdata/README.md)
//     and refused archives and paths.
//   - archive_validation_test.go (external): unsafe or inconsistent
//     content, malformed file manifests, the bounds and reader failures,
//     and the dependency and identity grammar.
//   - handler_test.go: the hooks' failure boundaries, a reindex that pages
//     and repairs properties, and admission of canonical validated content
//     only.
//   - contract_test.go (external): the format contract
//     (artifacts/core/formattest), with NoRemoteCache, because the hooks
//     leave a remote's cached archives out of the index.
package ansible
