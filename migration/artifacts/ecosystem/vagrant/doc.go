// Package vagrant validates Vagrant boxes and indexes their deployment
// properties. A box is one archive, a TAR, gzip-compressed TAR or ZIP at
// any .box path, deployed with exactly one box_name, box_provider and
// box_version property; its root metadata.json, read from the stored
// bytes, must name the provider box_provider does. Once a box is stored,
// its provider, architecture and SHA-256 are recorded as derived
// properties, and the package index is rebuilt from the properties alone.
//
// # Role
//
// artifacts module, ecosystem layer: the Vagrant box validation and
// catalog index of the Vagrant slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every write and
// property change in a Vagrant repository reaches it whichever API carried
// it; the slice's http package (artifacts/ecosystem/vagrant/http) serves
// the box catalog over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for vagrant repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and the
//     protocol, which checks a box name (ValidName) and reads each box's
//     identity and provider from its properties (Verified).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the boxes, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex). The generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds, is the first
//     check Admit applies. A ZIP box is spooled to a temporary file, and its
//     central directory checked (platform/archivecheck) before it is read.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read a box once before its transaction
//     (Inspect), admit only a valid box with a valid identity whose
//     provider its metadata.json confirms (Admit), record a stored box's
//     derived properties and rebuild the index (Deployed), refuse a
//     client's change to a derived property (AdmitAnnotation), rebuild the
//     index when an identity property changes (Annotated) or a box is
//     deleted (Deleted), and read every stored box again before rebuilding
//     (Reindex).
//   - The package index: a package per box_name, with a version per
//     box_version, whose files are the boxes whose recorded properties
//     verify.
//   - The derived vagrant.provider, vagrant.architecture and vagrant.sha256
//     item properties on each stored box.
//   - The rules the protocol shares: Identity, Box, ValidName,
//     ValidComponent, Properties, Verified, ReadBox, the identity property
//     names (PropName, PropProvider and PropVersion) and MaxArchiveBytes.
//
// # Invariants
//
//   - A box is read to its end within fixed bounds (16 GiB compressed,
//     64 GiB expanded, 100,000 entries, a 64 KiB metadata.json and, for a
//     ZIP, a 32 MiB central directory) without extracting a path or running
//     its Vagrantfile. Its entries are regular files and folders, with no
//     link, special file or duplicate, and no absolute, non-canonical or
//     traversing path or one holding a backslash, NUL or colon. A TAR's
//     extension headers count toward the expanded bound, and nothing but
//     zero padding may follow its end; every ZIP member's checksum is
//     checked.
//   - Only local and federated repositories take a box, and every write but
//     a checksum deploy must be one (400). A box_name is <name> or
//     <namespace>/<name>, each part at most 128 letters, digits, dots,
//     underscores and hyphens, starting with a letter or digit; a
//     box_version is a dotted version of at most 128 bytes, with an
//     optional hyphenated pre-release.
//   - The index is rebuilt from the recorded properties alone, so a
//     property change never decompresses a VM disk; a box whose recorded
//     SHA-256 or provider no longer agrees with it is not indexed. Only the
//     hooks write the derived properties (400 for a client's change), and
//     the items keep every property name, so the names never change.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, which this format refuses: Admit takes a box only in a
//     local or federated repository).
//   - docs/testing/vagrant.md (publishing and installing, updates and the
//     lifecycle, the archive checks and their bounds, and the evidence).
//   - JFrog Vagrant repositories:
//     https://docs.jfrog.com/artifactory/docs/vagrant-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/vagrant), line 385 (the slice row) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/785 and
//     https://github.com/weaveplatform/artifactweave/issues/1049
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - box_test.go: TAR, gzip-compressed TAR and ZIP boxes and their
//     identity, unsafe and corrupt input, hidden TAR extension headers, the
//     payload and padding bounds, the entry limit, cancellation and
//     resource failures.
//   - handler_test.go: admission's boundaries, index failures that
//     propagate, and a reindex that pages and skips boxes whose properties
//     do not verify.
//   - contract_test.go (external, vagrant_test): the format contract
//     (artifacts/core/formattest), with the box_* properties a client's
//     upload carries.
package vagrant
