// Package p2 validates Eclipse p2 update-site paths and bounded metadata.
// It classes the paths a p2 client reads (Classify), checks a p2.index,
// metadata file, artifact or site archive in full before a remote may
// cache or serve it (Validate, ExtractArchive), reads the checksums an
// update site declares for an artifact (ArtifactChecksums), and checks and
// rewrites the child locations of composite metadata (ValidChildLocation,
// EditComposite), all within fixed bounds. It also reads a P2 virtual
// repository's p2Urls entries and finds the member that serves each
// (ParseSiteURL, Covers, SiteChild).
//
// # Role
//
// artifacts module, ecosystem layer: the Eclipse p2 update-site path and
// bound rules of the P2 slice. A P2 repository is remote or virtual and is
// never deployed to, so the slice registers no format Handler and the item
// service gives P2 repositories the generic hooks (decision 0032). The P2
// protocol is the http subpackage (p2http), which serves a remote's update
// site, its site.zip!/ archive form and its composite children, and a
// virtual repository's composite metadata, with this package.
//
// # Inputs
//
//   - Driving: the protocol (p2http), which classes each read (Classify),
//     serves a file from a remote's site archive (ExtractArchive, within
//     MaxArtifact), resolves a composite's children (ValidChildLocation,
//     MaxChildURL, MaxDepth), rewrites their locations to the repository's
//     own paths (EditComposite), pins a child artifact to the checksums
//     its site declares (ArtifactChecksums) and serves entries of a child's
//     site archive (ExtractArchive); and the item kit
//     (artifacts/adapters/http/itemkit), whose remote source request checks
//     upstream p2 content before a remote caches it (Classify, Validate).
//     The protocol builds a virtual repository's composite from its
//     p2Urls, and the repositories core (artifacts/core/repos) plans the
//     members a P2 virtual repository's p2Urls need, with ParseSiteURL,
//     Covers and SiteChild.
//   - Driven: none. It reads only the bytes or reader it is given, with
//     the shared xz reader (platform/xzfile) for the .xml.xz metadata form,
//     and github.com/ulikunitz/xz to write it, and keeps no state.
//   - Settings: none. A remote's url (an update-site directory, or a
//     site.zip!/ archive) and its p2ChildOrigins, the other origins its
//     composite children may name, are read by the protocol.
//
// # Outputs
//
//   - The path rules: Kind with KindInvalid, KindIndex, KindMetadata and
//     KindArtifact, Classify, and ValidChildLocation.
//   - The content rules: Validate, ExtractArchive, ArtifactChecksums and
//     EditComposite, which fail with ErrInvalid.
//   - The p2Urls rules: SiteURL and ParseSiteURL, which fails with
//     ErrInvalid, Covers, Member and SiteChild.
//   - The bounds: MaxMetadata, MaxArtifact, MaxChildren, MaxDepth and
//     MaxChildURL.
//
// # Invariants
//
//   - Content is read in full before it is trusted: every member of a
//     metadata or artifact JAR is inflated and its CRC checked, an xz
//     stream is read to its end, and metadata XML is parsed to its last
//     token (at most 500,000). A site archive's entry list is checked
//     whole before the one entry served is read and validated.
//   - Metadata is a repository element whose type names the metadata or
//     artifact repository family its file name implies, a Composite type
//     for a composite file, with at most MaxChildren children and no
//     <!...> directive such as a DOCTYPE.
//   - A p2.index, at most 64 KiB, gives version=1 and both factory orders,
//     each naming only metadata files or "!".
//   - A child location is a relative path not starting with /, or an
//     absolute http or https URL with a host. It has no user information
//     or fragment, no backslash, NUL, CR or LF, and no "." or ".."
//     segment, encoded or not, within MaxChildURL bytes; the protocol also
//     checks its origin.
//   - EditComposite returns the whole rewritten file in its own encoding
//     or nothing, so no original child location reaches a client when one
//     replacement fails.
//   - An artifact's declared checksums are lowercase hex SHA-256 and
//     SHA-512 digests. An artifact the metadata does not list, lists twice
//     or declares a malformed digest for fails rather than going unpinned;
//     one listed without digests is left to the structural checks.
//   - The bounds match docs/testing/p2.md, which publishes them.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (a P2 remote's reads cross the shared source boundary and cache);
//     decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks, which P2 repositories take from generic).
//   - docs/testing/p2.md (connecting Eclipse, remote and virtual
//     repositories, composite children and their origins, the bounds and
//     the acceptance evidence).
//   - JFrog P2 repositories:
//     https://docs.jfrog.com/artifactory/docs/p2-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/p2), line 379 (the slice row), line 408 (the
//     protocol's home, ecosystem/p2/http) and line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - RTFACT-29915, the local://<key>/<suffix> form of a p2Urls entry,
//     and RTFACT-7631, a site served under its remote's key and sub-path:
//     https://jfrog.atlassian.net/browse/RTFACT-29915 and
//     https://jfrog.atlassian.net/browse/RTFACT-7631.
//   - https://github.com/weaveplatform/artifactweave/issues/773,
//     https://github.com/weaveplatform/artifactweave/issues/1043 and
//     https://github.com/weaveplatform/artifactweave/issues/1257
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Moved here in P9d.
//
// # Testing
//
//   - p2_test.go: the published Eclipse GLSP metadata and its artifact
//     checksum, each metadata encoding and its bounds, archive extraction
//     and untrusted paths, unsafe metadata, composite editing, p2.index
//     orders, broken archive entries, read failures, bad CRCs, unknown
//     compression, interrupted JAR reads and the XML token bound.
//   - external_coverage_test.go: artifact checksums across the published
//     encodings, missing archive entries, composite rewrites that keep a
//     JAR's other members and fail closed, index and archive integrity,
//     metadata read errors, declared and total expansion limits, and
//     corrupt member bytes.
//   - urls_test.go: the p2Urls entries ParseSiteURL accepts and refuses,
//     and the member and child SiteChild gives each.
//   - xz_test.go: .xml.xz metadata whose xz stream lost its index and
//     footer, refused wherever it is read (#1331), and a block header that
//     names a 1.5 GiB dictionary read within MaxMetadata, or one that names
//     4 GiB refused (#1332).
//   - xz_budget_test.go: that metadata, whole, cut and with a 1.5 GiB
//     header, answers the same under the xz dictionary budget as without
//     it, in Validate, ArtifactChecksums and EditComposite (#1354).
//
// testdata holds unmodified files of the published Eclipse GLSP 2.7.0 update
// site (p2.index, the content and artifacts metadata as .jar and .xml.xz,
// and a source bundle), which the suites in test/server/artifacts/p2, the
// protocol's tests (http/p2_test.go) and the acceptance steps also read.
// The protocol's native evidence is the @p2 acceptance partition
// (test/acceptance/features/p2.feature), which drives the Eclipse p2
// director.
package p2
