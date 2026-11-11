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
// and podspecs checked here. Its Git fetching builds a Git-sourced pod's
// archive for core remote's shared retrieval and cache policy, which this
// package imports and which reaches it only through the Policy.GitFetch
// hook (decision 0044).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for CocoaPods repositories to Handler; the
//     protocol package (cocoapods/http), which builds and parses CDN and
//     archive paths and reads the podspec an archive was indexed with
//     (Archive); and the remote policy (core remote), which checks an
//     HTTP-sourced pod's archive before caching it (ValidateSourceArchive)
//     and hands a Git-sourced pod's request, with its Git descriptor
//     (source.CocoaPodsGitSource), to FetchCocoaPodsGit through
//     Policy.GitFetch.
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler opens stored archives and keeps the package index; and the
//     remote.Policy that FetchCocoaPodsGit is given: its pooled upstream
//     client, its current-authority check (CheckCurrent) and the
//     repository's allowed origins (externalDependenciesPatterns).
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
//   - FetchCocoaPodsGit: the deterministic tar.gz of a Git-sourced pod's
//     revision and its pinned submodules, answered as an upstream response.
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
//   - FetchCocoaPodsGit never launches Git or writes a worktree: it reads
//     objects in memory over smart HTTP(S), and validates object IDs, pack
//     and delta expansion limits and archive members before the one
//     archive it builds enters the cache. The repository's credentials are
//     never sent to another Git origin, a submodule's included.
//   - Refusal texts are answered to clients in the response body, so they
//     stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks and optional interfaces; CocoaPods Git sources, #771).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (the GitFetch hook).
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
//   - https://github.com/weaveplatform/artifactweave/issues/771,
//     https://github.com/weaveplatform/artifactweave/issues/1016 and
//     https://github.com/weaveplatform/artifactweave/issues/1017
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Moved here in P9d.
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
//   - remote_git_test.go, remote_git_pack_test.go, remote_git_failure_test.go,
//     remote_git_limits_test.go and remote_git_authority_test.go: revisions,
//     tree and module safety, the pack reader, invalid object graphs,
//     upstream failures, resource limits, credential isolation and
//     revocation during a submodule fetch, against fake Git upstreams, some
//     serving the packs in test/fixtures/cocoapods-git; remote_fixture_test.go
//     holds the remote policy fixture they share with nothing else.
package cocoapods
