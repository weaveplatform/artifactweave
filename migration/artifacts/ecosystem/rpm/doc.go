// Package rpm reads bounded RPM v4 headers for native repository metadata.
// It checks every package a repository stores or caches, keeps the
// package index and the rpm.* item properties in step with the packages,
// and derives the repodata (repomd.xml and a checksum-named primary
// index) of each metadata root of a local or federated repository, with a
// detached GPG signature of repomd.xml when the repository names a key
// pair.
//
// # Role
//
// artifacts module, ecosystem layer: the RPM v4 header, repodata and
// signing rules of the RPM slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write and delete in
// an RPM repository reaches it whichever API carried it, and it rebuilds
// the repodata as queued work (decision 0033). The RPM protocol is still
// the root file internal/server/rpm.go, which composes a virtual
// repository's signed repodata and selects its packages with this
// package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for rpm repositories to Handler; the work queue,
//     which runs the repodata rebuild (KindIndex) through Handler.Work and
//     every format's reindex through Handler.Reindex, a local or federated
//     repository's Yum calculation (POST /api/yum/{repoKey}) included; and
//     the server's root files: the protocol (rpm.go), which reads member
//     repodata (DecodeRepomd, DecodePrimary) and writes a virtual
//     repository's (EncodeMetadata, SignRepomd), reindex.go, whose Yum
//     calculation of a virtual repository checks that its view still
//     composes (EncodeMetadata), source.go, which checks upstream repodata
//     or a package before a remote caches it (DecodeRepomd,
//     IsPrimaryMetadataPath, DecodePrimary, ReadPackage), and items.go,
//     which sends a remote's downloads through the remote source (Serves).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the packages; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), and lists a repository's
//     files by AQL (artifacts/core/aql); Enqueue asks for the repodata
//     rebuild when the write commits; Derive and Underive write and remove
//     the repodata; Now stamps its revision; and Keys (access/api) reads
//     the GPG signing pair, which the OpenPGP library
//     (github.com/ProtonMail/go-crypto) unlocks and signs with. zstd
//     (github.com/klauspost/compress) reads an upstream's zstd primary
//     index. The generic deploy policy (artifacts/ecosystem/generic) is
//     the first check Admit applies.
//   - Settings: none. A repository's calculateYumMetadata turns on the
//     automatic repodata rebuild after writes and deletes, its
//     yumRootDepth sets the depth of its metadata roots, and its
//     primaryKeyPairRef (keyPair for a virtual repository) names its
//     signing pair; a repository that names none publishes unsigned
//     repodata.
//
// # Outputs
//
//   - Handler: the hooks that read a package before its transaction
//     (Inspect), admit only a valid package at a valid path and refuse a
//     write of repodata to a repository that derives its own (Admit), keep
//     the package index and properties in step with deploys and deletes
//     (Deployed, Deleting, Deleted), class repodata as derived (Classify),
//     make a repodata read wait for its rebuild (Owed), derive the
//     repodata (WorkKinds, Work) and rebuild the package index (Reindex).
//   - The package rules: Package, Dependency and ReadPackage.
//   - The repodata rules: Metadata, EncodeMetadata, DecodeRepomd,
//     IsPrimaryMetadataPath, DecodePrimary and SignRepomd.
//   - The stored names: KindIndex and the Prop* property names.
//
// # Invariants
//
//   - A package's path is clean and relative, its every folder a safe
//     name, and its file name <name>-<version>-<release>.<architecture>.rpm
//     as its header gives them: a file name alone never gives a package
//     its identity.
//   - Each header stays within 16 MiB and 100,000 entries, and the payload
//     must match the SHA-256 the header gives when it gives one.
//   - A package index version is <version>-<release>.<architecture>, so the
//     builds of one version for each architecture are versions of their
//     own.
//   - Repodata is per metadata root, the folder yumRootDepth levels below
//     the repository root (the root itself by default); a package not
//     below that depth is in no root and is left out. The primary index is
//     gzip-compressed and named by its SHA-256, and repomd.xml gives that
//     digest and the uncompressed one.
//   - repomd.xml is written last, after the primary index and the
//     signature it is checked against, so a reader never sees a manifest
//     whose files are missing; the signature (repomd.xml.asc, with the
//     public key as repomd.xml.key) covers the metadata, never the
//     packages.
//   - With calculateYumMetadata off, writes and deletes keep the package
//     index current but queue no repodata rebuild; a Yum calculation
//     (Reindex) always queues one.
//   - A local or federated repository's repodata is derived, never
//     uploaded; a remote caches the upstream's after source.go has checked
//     it: repomd.xml must name a checksum-named primary index under
//     repodata/, and the primary index, gzip or zstd, must match that
//     checksum and stay within 36 MiB.
//   - The repodata work takes at most 100,000 packages per repository.
//   - KindIndex and the property names are stored, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and optional interfaces); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the repodata
//     rebuild runs as queued work).
//   - docs/testing/rpm.md (connecting dnf or yum, calculateYumMetadata and
//     yumRootDepth, signing and the acceptance evidence) and
//     test/acceptance/testdata/rpm-upstream/README.md (the independent
//     createrepo_c repodata).
//   - JFrog RPM repositories:
//     https://docs.jfrog.com/artifactory/docs/rpm-repositories, and the
//     Yum calculation API:
//     https://docs.jfrog.com/artifactory/reference/calculateyumrepositorymetadata.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/rpm), line 379 (the slice row), line 405 (the
//     protocol's later home, ecosystem/rpm/http) and line 997 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/759 and
//     https://github.com/weaveplatform/artifactweave/issues/1041
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760). Staged in P9d.
//
// # Testing
//
//   - package_test.go and corruption_test.go: ReadPackage on a genuine
//     rpmbuild package, storage failures at each boundary, and corrupted
//     real headers.
//   - decode_test.go and metadata_test.go: untrusted repomd references
//     and primary packages, dependency constraints through the primary
//     index, unsafe package records, malformed or truncated compressed
//     XML, the independent createrepo_c repodata, and EncodeMetadata from
//     an rpmbuild package.
//   - sign_test.go: SignRepomd binds the metadata it signs.
//   - work_validation_test.go: Admit's protection of generated repodata
//     and package identity, the configured root depth, the repodata work
//     and its publication failures, signing through Env.Keys and wrong key
//     types, storage failures, missing content and stale inspections.
//   - contract_test.go: the format contract suite (artifacts/core/formattest)
//     with KindIndex as the repository's work.
//
// testdata holds the contract RPMs that build-contract-fixtures.sh makes in
// the pinned Fedora 42 image, and the genuine Fedora 42 package
// artifactweave-alpha-1.0-1.fc42.noarch.rpm with its alpha.spec, which
// internal/server's rpm_internal_test.go and the acceptance steps also read.
// The protocol's native dnf evidence is the @rpm acceptance partition
// (test/acceptance/features/rpm.feature).
package rpm
