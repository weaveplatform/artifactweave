// Package npmhttp serves the npm registry of npm repositories at
// /artifactory/api/npm/{repoKey}/ (decision 0034), mounted through the
// router kit (decision 0032). Packuments and version manifests are built on
// each request from what the npm format keeps in the package index, and
// tarballs download through the item routes' download, so redirects and
// download counts apply to them as to any file. A publish stores the
// tarball through the item service, whose npm format indexes it as it
// indexes a REST deploy. Beside the registry, the npm partial strict server
// (NPMAPI) answers the JFrog operations that hand npm its .npmrc lines and
// queue an npm local repository's reindex and strict-layout conversion.
//
// # Role
//
// artifacts module, ecosystem layer: the native npm registry protocol of the
// npm slice, and its HTTP layer for the JFrog operations beside it. It is
// named npmhttp so that it does not shadow net/http. Outside tests, only its
// own http subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type npm: packuments, version
//     manifests and tarballs; publish, dist-tags, deprecation and
//     unpublish; search, ping and whoami; and npm's legacy login and
//     logout. The router also embeds NPMAPI in the artifactory spec's
//     strict server, calls NPMOperation and IsNPMReindex in its
//     error-profile rules, and gives the authentication middleware
//     NPMUserRequest as its first skip and KeepNPMCredential as its
//     credential keeper, and the access log NPMLoggedPath after the
//     LuaRocks redactor.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads and writes items and writes
//     errors; the format package (artifacts/ecosystem/npm), whose index and
//     rules the answers are built from; the accounts (Options.Accounts),
//     whose password check and token service the access module provides
//     (access/api); and the remote source service (Options.RemoteSources),
//     which fetches upstream metadata and tarballs into the remote cache.
//     NPMAPI's operations run over the item kit
//     (artifacts/adapters/http/itemkit), whose store (itemkit.Store) holds
//     the parked conversion that a resume reads, and which resolves the
//     repository, checks Manage and queues the reindex every format shares,
//     and over access/api's Tokens, whose encrypted password stands in for a
//     password caller's own in the .npmrc lines (DV-004).
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the npm error profile; DefaultMaxPublishSize and
//     DefaultLoginTokenExpiry, decision 0034's defaults.
//   - Accounts, Passwords and Tokens: what login and logout need from the
//     server.
//   - NPMAPI: the npm partial strict server, whose five operations answer
//     the .npmrc lines (GetNpmAuth, GetNpmScopeAuth) and queue an npm local
//     repository's reindex and strict-layout conversion
//     (CalculateNpmRepositoryMetadata, MigrateNpmRepositoryToStrictLayout,
//     ResumeNpmRepositoryMigration).
//   - NPMOperation and IsNPMReindex, the paths that answer errors in
//     Artifactory's envelope; NPMUserRequest, the login and logout paths
//     the middleware reads no credentials on; KeepNPMCredential, which
//     keeps the token a request authenticated with for the .npmrc lines;
//     and NPMLoggedPath, which redacts the token of a logout path in the
//     access log.
//   - Credential, AuthLines, ScopeAuthLines and ValidScope: the .npmrc
//     lines that GetNpmAuth and GetNpmScopeAuth answer.
//   - Answers: for a local or federated repository, packuments and
//     manifests built from the package index, with what the caller may not
//     read left out; for a remote repository, the upstream metadata with
//     only the tarball URLs rewritten and its signatures kept, and checked
//     tarballs.
//
// # Invariants
//
//   - A packument, manifest, search result or tag list shows only the
//     versions the caller may read.
//   - Remote metadata is cached as the upstream sent it; an answer
//     rewrites only its tarball URLs and keeps its signature fields, and a
//     tarball's integrity is checked before it is published or served from
//     the cache.
//   - A write to a remote repository answers 405, and statuses and error
//     texts stay byte-identical: npm reads them.
//   - A caller's raw token reaches no handler but the .npmrc operations:
//     only KeepNPMCredential stores it in the request's context, under a
//     key private to this package, and only NPMAPI reads it back.
//
// # References
//
//   - Decision 0034: docs/research/decisions/0034-npm.md (routes,
//     packuments, publishing, dist-tags, login and tokens, and "P9 shared
//     retrieval contract").
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md;
//     decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit).
//   - docs/operations/npm-remote.md (remote repositories).
//   - JFrog npm repositories, as decision 0034 cites it:
//     https://docs.jfrog.com/artifactory/docs/npm-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "npm ecosystem" (lines 413 to 445), line 402 (the root npm_auth.go
//     and npm_operations.go, now login.go and operations.go) and line 1017
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epics ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943) and WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/626 (the remote
//     registry adapter),
//     https://github.com/weaveplatform/artifactweave/issues/1031,
//     https://github.com/weaveplatform/artifactweave/issues/1086 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P7, with remote retrieval in P9. Staged in P9d, which also
// moved the server root's npm operations here (WEB-17).
//
// # Testing
//
//   - npm_test.go, publish_test.go, lifecycle_test.go, latest_test.go and
//     search_test.go: the registry routes through a server, their
//     permissions, refusals and store faults.
//   - user_test.go: login and logout with the real authenticator and token
//     service, built by the access test kit (access/accesstest).
//   - remote_test.go, remote_dist_test.go and remote_integrity_test.go:
//     remote metadata, tarball integrity, URL policy and outages.
//   - npmrc_test.go: the .npmrc lines and scopes.
//   - login_test.go: the login and logout path predicates, the
//     configuration operations' paths and the logged-path redactor.
//   - operations_test.go: NPMAPI's operations without the router, with no
//     store, no caller and a store that fails under them; fixture_test.go
//     holds their fixtures.
package npmhttp
