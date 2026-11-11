// Package cocoapodshttp serves the current CocoaPods CDN protocol through
// /artifactory/api/pods/{repoKey}: the version document, the pod list, the
// sharded version lists and the podspecs that pod repo add-cdn and pod
// install read, and the archives those podspecs point at. The retired Git
// specs plugin is not a second protocol surface. Names, paths and archives
// are the format package's (artifacts/ecosystem/cocoapods); this package
// answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native CocoaPods CDN protocol of
// the CocoaPods slice. It is named cocoapodshttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type cocoapods: GET and HEAD
//     for every CDN path of a local, remote, virtual or federated
//     repository.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads items and remote
//     sources and writes errors; the remote source service
//     (Options.RemoteSources), which fetches upstream CDN metadata and pod
//     source archives (over HTTP, or from Git) into the remote cache; and
//     the format package, which checks names, versions and archives.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: for a local or federated repository, CocoaPods-version.yml,
//     all_pods.txt, the sharded version lists and the podspecs built from
//     the package index, and the archives; for a remote repository, the
//     upstream CDN metadata, checked whole, with each podspec's source
//     pointed at this server, and the source archives; for a virtual
//     repository, its members in priority order.
//
// # Invariants
//
//   - A list, podspec or archive answers only what the caller may read: a
//     version the caller may not read is left out of every list.
//   - Upstream CDN metadata and source archives are checked whole before
//     they are cached, and a source archive is fetched only from an origin
//     the repository allows (externalDependenciesEnabled and its patterns).
//   - Statuses and error texts stay byte-identical: the pod client reads
//     them.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers).
//   - docs/testing/cocoapods.md (connecting pod, Git sources and
//     submodules, static Ruby podspecs, and the evidence).
//   - JFrog CocoaPods repositories and the repository class table, as that
//     page cites them:
//     https://docs.jfrog.com/artifactory/docs/cocoapods-repositories and
//     https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/cocoapods) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/771,
//     https://github.com/weaveplatform/artifactweave/issues/1016 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Moved here in P9d.
//
// # Testing
//
//   - route_test.go: local, remote and virtual CDN routes, path and
//     response boundaries, permissions, paging and corrupt metadata, with
//     the access test kit (access/accesstest) for permissions.
//   - remote_test.go: remote podspec and index checks, the redirect and
//     origin guards and the source URL bounds.
//   - git_source_test.go: Git-sourced pods cold, through a virtual
//     repository and offline, failures not cached, and mutable tags.
//   - metadata_failure_test.go and zip_source_test.go: metadata stream and
//     read failures, the authority recheck before delivery and zip path
//     traversal.
//   - The @cocoapods acceptance scenarios
//     (test/acceptance/features/cocoapods.feature) drive the pod client
//     through the whole server.
package cocoapodshttp
