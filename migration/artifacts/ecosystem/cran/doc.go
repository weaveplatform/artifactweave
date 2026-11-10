// Package cran implements the CRAN package, index and archive rules. It
// checks every R package archive a repository stores or caches against its
// path and DESCRIPTION, keeps the package index and the cran.name,
// cran.version and cran.feed item properties in step with the packages,
// and derives a local or federated repository's PACKAGES and PACKAGES.gz
// for each feed directory R reads. It also reads the PACKAGES indexes that
// remote and virtual reads rely on, and finds the source archives still in
// the legacy archive layout.
//
// # Role
//
// artifacts module, ecosystem layer: the CRAN package, index and archive
// rules of the CRAN slice. Its Handler plugs into the item service through
// the format hooks (decision 0032), so every write and delete in a CRAN
// repository reaches it whichever API carried it, and it rebuilds the
// indexes as queued work (decision 0033). The CRAN protocol is the http
// subpackage (cranhttp), which serves every class's index, the native POST
// uploads and the legacy archive migration with this package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for cran repositories to Handler; the work
//     queue, which runs the index rebuild (KindIndex) through Handler.Work
//     and every format's reindex through Handler.Reindex; the protocol
//     (cranhttp), which answers an index read by decoding the member's
//     index (DecodeIndex: a local or federated repository's derived one
//     once its rebuild is done, a remote's from upstream) and encoding
//     again only the packages the caller may read, a virtual repository's
//     members merged in order (EncodeIndex), whose POST
//     /api/cran/{repoKey}/sources and binaries check the feed (Feed) and
//     take the package's identity from the archive (ReadUploaded,
//     ErrPackage), and whose archive migration (GET
//     /api/cran/{repoKey}/move-archives) finds what to move
//     (LegacyArchiveTarget); and the item kit
//     (artifacts/adapters/http/itemkit), whose remote source request checks
//     an upstream index or package before a remote caches it (IndexPath,
//     DecodeIndex, IsPackage, ReadPackage).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the packages; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), and lists a repository's
//     files by AQL (artifacts/core/aql); Enqueue asks for the index rebuild
//     when the write commits; and Derive and Underive write and remove the
//     indexes. xz (github.com/ulikunitz/xz) reads a .tar.xz source archive.
//     The generic deploy policy (artifacts/ecosystem/generic) is the first
//     check Admit applies.
//   - Settings: none. The archive migration's switch
//     (cran.archive_mover_enabled, off by default) belongs to the protocol
//     that serves it (cranhttp.CranAPI.ArchiveMoverEnabled).
//
// # Outputs
//
//   - Handler: the hooks that read a package before its transaction
//     (Inspect), admit only a valid package at a valid path and refuse a
//     write of an index to a repository that derives its own (Admit), keep
//     the package index and properties in step with deploys and deletes
//     (Deployed, Deleting, Deleted), class the indexes as derived
//     (Classify), make an index read wait for its rebuild (Owed), derive
//     the indexes (WorkKinds, Work) and rebuild the package index
//     (Reindex).
//   - The path rules: Serves, Feed, IndexPath, PackagePath, IsPackage,
//     ValidName and ValidVersion, and the index names PackagesName and
//     PackagesGzipName.
//   - The package rules: Package, ReadPackage and ReadUploaded, which fail
//     with ErrPackage.
//   - The index rules: EncodeIndex, DecodeIndex and CompareVersion.
//   - The archive rule: LegacyArchiveTarget.
//   - The stored names: KindIndex and the Prop* property names.
//
// # Invariants
//
//   - A package's name and version come from its file name,
//     <name>_<version> with its feed's suffix (.tar.gz, .tar.bz2 or .tar.xz
//     under src/contrib; .zip, .tgz and .tar.gz under the Windows, macOS and
//     Linux binary feeds), and its DESCRIPTION must give the same Package
//     and Version.
//   - An archive is read to its end, compressor checksums and ZIP CRCs
//     included, before its DESCRIPTION is trusted, within fixed bounds (1
//     GiB unpacked, 50,000 entries, a 256 KiB DESCRIPTION), and every entry
//     must lie in the package's own folder.
//   - A feed is a terminal directory: src/contrib, or
//     bin/<windows|linux|macosx>/contrib/<R major.minor>, macOS with an
//     optional build folder before contrib. Each feed has its own PACKAGES
//     and PACKAGES.gz, which list only the newest version of each package
//     (CompareVersion, R's version order) with the MD5 sum of its stored
//     bytes. A package in a folder below its feed, such as
//     src/contrib/Archive/<name>/, stays downloadable but is never listed.
//   - An index is at most 32 MiB and 100,000 records. A decoded index lists
//     each package once, every record naming a file of the index's own
//     feed, so an upstream index cannot point a client elsewhere.
//   - A local or federated repository's PACKAGES and PACKAGES.gz are
//     derived, never uploaded (409); a remote caches the upstream's after
//     the item kit's remote source request has checked it, and cached
//     content queues no rebuild.
//   - KindIndex and the property names are stored, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and optional interfaces, and its CRAN archive
//     migration section); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the index
//     rebuild runs as queued work).
//   - docs/testing/cran.md (connecting R, the upload forms, the archive
//     migration and the acceptance evidence).
//   - JFrog CRAN repositories:
//     https://docs.jfrog.com/artifactory/docs/cran-repositories, and the
//     archive migration API:
//     https://docs.jfrog.com/artifactory/reference/movecranarchives. The
//     feed layout R reads is in R Installation and Administration:
//     https://cran.r-project.org/doc/manuals/r-release/R-admin.html.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/cran), line 379 (the slice row), line 406 (the
//     protocol's home, ecosystem/cran/http) and line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/778 and
//     https://github.com/weaveplatform/artifactweave/issues/1043
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Staged in P9d.
//
// # Testing
//
//   - package_test.go: the path grammar, ReadPackage on the unchanged CRAN
//     source archive and Windows binary, archive identity and integrity,
//     unsafe ZIP entries, and ReadUploaded's identity and refusals.
//   - index_test.go: EncodeIndex's newest version and round trip, empty
//     feeds and size limits, excess records, untrusted paths and
//     duplicates.
//   - archive_test.go: LegacyArchiveTarget.
//   - edge_test.go: index ownership and the upload policy, untrusted
//     archive content, .tar.xz and binary indexes, the DESCRIPTION and
//     index trust boundary, cached and remote content queuing nothing,
//     untrusted index records, damaged compressed indexes and a canceled
//     ZIP read.
//   - work_test.go: the hooks and Work: inspection of committed bytes,
//     storage and work queue failures, regenerating only advertised
//     packages, removing a feed's index after its last package, archives
//     and unrelated files kept out of the index, and the refusals.
//
// testdata holds the unchanged CRAN assertthat 0.2.1 source archive and
// Windows binary and its published PACKAGES entry, which the suites in
// test/server/artifacts/cran, the protocol's tests (http/cran_test.go,
// cran_routes_test.go and cran_archive_test.go) and the acceptance steps
// also read. The
// protocol's native R evidence is the @cran acceptance partition
// (test/acceptance/features/cran.feature).
package cran
