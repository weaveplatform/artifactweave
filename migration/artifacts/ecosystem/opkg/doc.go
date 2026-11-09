// Package opkg reads bounded IPK archives and their control metadata. It
// checks every IPK a repository stores or caches, keeps the package index
// and the opkg.* item properties in step with the IPKs, and derives the
// Packages, Packages.gz and Release files of each feed of a local or
// federated repository, with a detached GPG signature of Packages
// (Packages.sig) when the repository names a key pair.
//
// # Role
//
// artifacts module, ecosystem layer: the IPK archive, Packages index and
// signing rules of the Opkg slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write and delete in
// an Opkg repository reaches it whichever API carried it, and it rebuilds
// the feed metadata as queued work (decision 0033). The Opkg protocol is
// still the root file internal/server/opkg.go, which builds a virtual
// repository's signed feed and selects its IPKs with this package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for opkg repositories to Handler; the work
//     queue, which runs the feed rebuild (KindIndex) through Handler.Work
//     and every format's reindex, the reindex operation's included (its
//     writeProps=0 sets Env.SkipProperties), through Handler.Reindex; and
//     the server's root files: the protocol (opkg.go), which reads member
//     feeds (DecodeIndex) and writes a virtual repository's (EncodeIndex,
//     SignFeed), and source.go, which checks upstream feed metadata or an
//     IPK before a remote caches it (IsIndex, FeedPath, DecodeIndex,
//     ValidateRelease, ValidPath, ReadPackage).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the IPKs; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), and lists a repository's
//     files by AQL (artifacts/core/aql); Enqueue asks for the feed rebuild
//     when the write commits; Derive and Underive write and remove the
//     feed metadata; and Keys (access/api) reads the GPG signing pair,
//     which the OpenPGP library (github.com/ProtonMail/go-crypto) unlocks
//     and signs with. The generic deploy policy (artifacts/ecosystem/generic)
//     is the first check Admit applies.
//   - Settings: none. A local or federated repository's primaryKeyPairRef,
//     or a virtual repository's keyPair, names its signing pair; a
//     repository that names none publishes unsigned feeds.
//
// # Outputs
//
//   - Handler: the hooks that read an IPK before its transaction
//     (Inspect), admit only a valid IPK at a valid path and refuse a write
//     of feed metadata to a repository that derives its own (Admit), keep
//     the package index and properties in step with deploys and deletes
//     (Deployed, Deleting, Deleted), class feed metadata as derived
//     (Classify), make a feed metadata read wait for its rebuild (Owed),
//     derive the feed metadata (WorkKinds, Work) and rebuild the package
//     index (Reindex).
//   - The package rules: Package, ValidPath, Feed and ReadPackage.
//   - The feed rules: PackagesName, PackagesGzipName, ReleaseName,
//     SignatureName, IsIndex, FeedPath, IndexPath, DecodeIndex,
//     EncodeIndex, ValidateRelease, Digest and SignFeed.
//   - The stored names: KindIndex and the Prop* property names.
//
// # Invariants
//
//   - An IPK is OpenWrt's gzip(tar) or the older ar container, holding
//     exactly debian-binary (2.0), control.tar.gz and data.tar.gz. Its
//     control file gives its name, version and architecture; a canonical
//     file name (two or more underscores) must be
//     <name>_<version>_<architecture>.ipk, while a noncanonical one such as
//     my-app.ipk is allowed, as Artifactory's upload example shows.
//   - A feed record's size and digests come from the stored bytes, never
//     from the IPK's control file.
//   - The signature covers the uncompressed Packages, which opkg verifies
//     before it trusts the SHA256sum fields inside; those fields then bind
//     each IPK it downloads.
//   - A local or federated repository's feed metadata is derived, never
//     uploaded; a remote caches the upstream's after source.go has checked
//     it. An upstream Packages must keep every IPK inside its feed, give
//     each a lowercase SHA-256 and a positive size, and name no file twice.
//   - An IPK is at most 1 GiB stored and 2 GiB expanded, its control file
//     at most 1 MiB; a Packages index is at most 36 MiB and 100,000
//     packages, and the feed work takes at most 100,000 IPKs per
//     repository.
//   - KindIndex and the property names are stored, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and optional interfaces); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the feed
//     rebuild runs as queued work).
//   - docs/testing/opkg.md (connecting opkg, signing and the acceptance
//     evidence, with the published OpenWrt fixtures' sources).
//   - JFrog Opkg repositories:
//     https://docs.jfrog.com/artifactory/docs/opkg-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/opkg), line 379 (the slice row), line 405 (the
//     protocol's later home, ecosystem/opkg/http) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/760 and
//     https://github.com/weaveplatform/artifactweave/issues/1041
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760). Staged in P9d.
//
// # Testing
//
//   - package_test.go and package_validation_test.go: the published
//     OpenWrt IPK, a corrupted gzip trailer, the legacy ar container and
//     malformed archives, paths and control fields, nested corruption,
//     member headers and padding, special member types and truncation, and
//     a control identity that cannot override a canonical file name.
//   - index_test.go and index_validation_test.go: the published OpenWrt
//     Packages records, round trips with Release, unsafe records, invalid
//     encoder input, and the digest and Release trust boundaries.
//   - handler_validation_test.go: admission and derived ownership,
//     deletion and work failures, SignFeed's binding and bad keys, the
//     feed work signing through Env.Keys, and missing or uninspectable
//     IPKs and a failing package inventory.
//   - contract_test.go: the format contract suite (artifacts/core/formattest)
//     with KindIndex as the repository's work.
//
// testdata holds the contract IPKs that build-contract-fixtures.sh makes
// and the published OpenWrt 24.10.0 index and IPK, which the root
// internal/server/opkg_test.go and the acceptance steps also read;
// .gitattributes keeps openwrt-Packages with LF line ends and its final
// blank line on every OS. The protocol's native opkg evidence is the @opkg
// acceptance partition (test/acceptance/features/opkg.feature).
package opkg
