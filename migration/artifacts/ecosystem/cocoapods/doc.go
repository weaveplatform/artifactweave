// Package cocoapods is the format of CocoaPods repositories: the pod
// archive and its podspec, the names and paths of the CocoaPods CDN, and
// the package index. A hosted pod version is one archive at
// <name>/<version>/<name>-<version>.tar.gz whose one root podspec (JSON,
// or static Ruby read as data) names that pod and version; the CDN shards
// a pod's specs and version list by the MD5 of its name.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the CocoaPods
// slice (package type cocoapods). Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write and delete in a
// CocoaPods repository reaches it whichever API carried it. The slice's
// protocol package, cocoapods/http, serves the CDN with the names, paths
// and podspecs checked here.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for CocoaPods repositories to Handler; the
//     protocol package (cocoapods/http), which builds and parses CDN and
//     archive paths and reads the podspec an archive was indexed with
//     (Archive); and remote retrieval, which checks an HTTP-sourced pod's
//     archive before caching it (ValidateSourceArchive) and bounds a
//     Git-sourced one (MaxArchiveBytes).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler opens stored archives and keeps the package index.
//
// # Outputs
//
//   - Names and paths: ValidName, ValidVersion, Path, ParsePath, Shard,
//     ShardFile and SpecPath.
//   - Archives: ReadArchive, which returns the Archive of a valid hosted
//     pod archive, MaxArchiveBytes, and ValidateSourceArchive for a remote
//     pod's source archive (zip or tar.gz).
//   - Handler: the hooks that inspect and admit a deployed archive, index
//     its version with the Archive as metadata, and rebuild the index from
//     the stored archives (Reindex).
//
// # Invariants
//
//   - An archive is admitted only when every tar header and path, the gzip
//     trailer and its one root podspec are valid and the podspec names the
//     archive's pod and version, so a corrupt or path-escaping archive never
//     becomes a resolvable version.
//   - A Ruby podspec is parsed as data and never executed; a statement
//     outside the static grammar fails the upload.
//   - CDN metadata under .pod/cdn/ is generated, so a deploy there is
//     refused with 409.
//   - Refusal texts are answered to clients in the response body, so they
//     stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks and optional interfaces).
//   - docs/testing/cocoapods.md (the CDN client, Git sources and static
//     Ruby podspecs).
//   - JFrog CocoaPods repositories and the repository class table, as that
//     page cites them:
//     https://docs.jfrog.com/artifactory/docs/cocoapods-repositories and
//     https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/cocoapods), line 379 (one slice per package type)
//     and line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/771 and
//     https://github.com/weaveplatform/artifactweave/issues/1016
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Staged in P9d.
//
// # Testing
//
//   - archive_test.go and archive_security_test.go: archive identity and
//     integrity, hidden PAX expansion, truncated members and the source
//     archive bounds, zip compression and cancellation.
//   - ruby_spec_test.go: the static Ruby podspec grammar and its refusals.
//   - handler_test.go and handler_lifecycle_test.go: admission against the
//     stored archive, a cached HTTP source outside the hosted grammar, and
//     reindexing.
package cocoapods
