// Package debian implements Debian package and repository metadata. It
// checks every binary (.deb, .ddeb) and source (.dsc) package a repository
// stores or caches, keeps the package index and the deb.name and
// deb.version item properties in step with the binaries, and derives a
// local or federated repository's apt metadata: for each distribution, the
// Packages and Sources indexes of every component and architecture with
// their gzip forms and by-hash copies, and a Release file, signed as
// InRelease and Release.gpg when the repository names a key pair. It also
// reads and checks the upstream apt metadata that remote, virtual and
// snapshot reads rely on.
//
// # Role
//
// artifacts module, ecosystem layer: the Debian package, index, source and
// signing rules of the Debian slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every write and
// delete in a Debian repository reaches it whichever API carried it, and
// it rebuilds the apt metadata as queued work (decision 0033). The Debian
// protocol is the http subpackage (debianhttp), which serves remote and
// virtual metadata, the reindex operation and snapshots with this package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the item
//     service's hooks for debian repositories to Handler; the work queue, which
//     runs the metadata rebuild (KindIndex) and the reindex operation's
//     writeProps=0 form (KindReindexNoProps) through Handler.Work, and every
//     format's reindex through Handler.Reindex; the protocol (debianhttp),
//     which checks metadata paths and decodes an index to serve it only to a
//     caller who may read every package it lists, composes and signs a
//     virtual repository's metadata (DecodePackagesIndex,
//     ReleasePackageIndexes, GzipIndex, Release, SignRelease), copies
//     checksum-bound metadata into a snapshot (ReleaseIndexRefs) under
//     WithSnapshotWrite, and queues KindReindexNoProps; the item kit
//     (artifacts/adapters/http/itemkit), whose remote source request checks
//     upstream metadata or a package before a remote caches it (the *Path
//     predicates, ValidateReleaseMetadata, DecodePackagesIndex,
//     DecodeSourcesIndex, ReadBinary, ReadSource), and whose item GET sends a
//     remote's downloads through the remote source (Serves); and the
//     artifacts module's item delete (artifacts/adapters/http), which permits
//     a whole snapshot folder's delete (WithSnapshotDelete).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the packages; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), and lists a repository's
//     files by AQL (artifacts/core/aql); Enqueue asks for the metadata
//     rebuild when the write commits; Derive and Underive write and remove
//     the metadata; Now dates each Release; and Keys (access/api) reads
//     the GPG signing pair, which the OpenPGP library
//     (github.com/ProtonMail/go-crypto) unlocks and signs with. xz
//     (github.com/ulikunitz/xz) and zstd (github.com/klauspost/compress)
//     read a package's compressed control and data members. The generic
//     deploy policy (artifacts/ecosystem/generic) is a check Admit
//     applies.
//   - Settings: none. A repository's primaryKeyPairRef (keyPair for a
//     virtual repository) names its signing pair, ddebSupported admits
//     .ddeb debug packages, and debianTrivialLayout refuses source
//     packages.
//
// # Outputs
//
//   - Handler: the hooks that read a package before its transaction
//     (Inspect), admit only a valid package with valid coordinates and
//     keep clients out of apt metadata and snapshots (Admit, Deleting),
//     keep the package index and properties in step with deploys and
//     deletes (Deployed, Deleting, Deleted), class apt metadata as derived
//     (Classify), make a metadata read wait for its rebuild (Owed), derive
//     the metadata (WorkKinds, Work) and rebuild the package index
//     (Reindex).
//   - The package rules: Binary and ReadBinary; Source, SourcePart and
//     ReadSource.
//   - The index rules: IndexedBinary and Packages, IndexedSource,
//     SourceArchive and Sources, GzipIndex, ReleaseEntry and Release,
//     SignedRelease and SignRelease, ValidBinaryPath and ValidSourcePath.
//   - The upstream metadata rules: PackagesIndexPath, SourcesIndexPath,
//     ReleaseMetadataPath, ByHashMetadataPath, ValidateReleaseMetadata,
//     PackageIndexRef with ReleasePackageIndexes and ReleaseIndexRefs,
//     PackageRecord with DecodePackagesIndex and ParsePackagesIndex, and
//     SourceIndexRecord with DecodeSourcesIndex.
//   - Snapshot guards: WithSnapshotWrite and WithSnapshotDelete.
//   - The stored names: KindIndex, KindReindexNoProps and the Prop*
//     property names.
//
// # Invariants
//
//   - A binary's identity comes from its control data, which must give a
//     valid Package, Version and Architecture and a Maintainer and
//     Description; its ar members must be debian-binary (2.x), then
//     control.tar and data.tar, within fixed bounds (8 GiB archive, 16 MiB
//     control member, 1 MiB control file, 4 GiB data).
//   - A binary is listed only under the deb.distribution, deb.component
//     and deb.architecture it was deployed with, which must name its own
//     architecture unless it is Architecture: all; one deployed without
//     them is stored but never indexed. An Architecture: all package is
//     listed under binary-all, under every other architecture of its
//     distribution and component, and under amd64 even when nothing else
//     names it, so a fresh amd64 apt client finds it.
//   - A source package is listed only once its .dsc carries
//     dsc.distribution and dsc.component and every archive it names is
//     stored beside it with the declared size and digests.
//   - Index records take sizes and digests from the stored bytes, never
//     from what a client uploaded; the Release file gives each index's
//     digests, and its signatures bind them.
//   - A local or federated repository's apt metadata under dists/ is
//     derived, never uploaded, and never built for a remote from its
//     partial cache: a remote caches the upstream's after the item kit's
//     remote source request has checked it.
//   - Nothing under snapshots/ is written except by the server's own
//     snapshot publication, or deleted except as a whole snapshot folder
//     (409).
//   - KindIndex, KindReindexNoProps and the property names are stored, so
//     they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and optional interfaces); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the metadata
//     rebuild runs as queued work).
//   - docs/testing/debian.md (connecting apt, deploy coordinates, source
//     packages, snapshots and the acceptance evidence).
//   - JFrog Debian repositories:
//     https://docs.jfrog.com/artifactory/docs/debian-repositories; the
//     metadata calculation API:
//     https://docs.jfrog.com/artifactory/reference/calculatedebianrepositorymetadata;
//     and the snapshot API:
//     https://docs.jfrog.com/artifactory/reference/createdebiansnapshot.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/debian), line 379 (the slice row), line 407 (the
//     protocol's home, ecosystem/debian/http) and line 997 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/758 and
//     https://github.com/weaveplatform/artifactweave/issues/1042
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760). Staged in P9d.
//
// # Testing
//
//   - binary_test.go, binary_security_test.go and real_fixture_test.go:
//     ReadBinary on dpkg-built packages, corrupt ar structure, malformed
//     control archives and ambiguous control fields.
//   - source_test.go and source_security_test.go: ReadSource on the
//     dpkg-source fixture and a signed descriptor, invalid control, corrupt
//     declarations and archive bindings, and Sources order.
//   - index_test.go, metadata_test.go and metadata_security_test.go:
//     Packages from stored digests in stable order, Release checksums,
//     compressed indexes, upstream Release and Packages checks, by-hash
//     paths and forged digests.
//   - sign_test.go: SignRelease binds both apt signature forms.
//   - handler_security_test.go: admission of generated metadata,
//     snapshots and package coordinates, stale inspections, class-specific
//     work, the source lifecycle and unavailable content.
//   - work_test.go and source_work_test.go: Release and every
//     architecture's index, signing through Env.Keys, and only complete
//     source packages published.
//   - contract_test.go: the format contract suite (artifacts/core/formattest)
//     with KindIndex as the repository's work.
//
// testdata holds the contract packages, the dpkg-source fixture alpha_1.0-1
// and generate-source.sh, which builds it; the protocol's tests
// (http/debian_source_test.go) and the acceptance steps also read them. The
// protocol's native apt evidence is the @debian acceptance partition
// (test/acceptance/features/debian.feature).
package debian
