// Package npmhttp serves the npm registry of npm repositories at
// /artifactory/api/npm/{repoKey}/ (decision 0034), mounted through the
// router kit (decision 0032). Packuments and version manifests are built on
// each request from what the npm format keeps in the package index, and
// tarballs download through the item routes' download, so redirects and
// download counts apply to them as to any file. A publish stores the
// tarball through the item service, whose npm format indexes it as it
// indexes a REST deploy.
//
// # Role
//
// artifacts module, ecosystem layer: the native npm registry protocol of
// the npm slice. It is named npmhttp so that it does not shadow net/http.
// Outside tests, only its own http subtree, the module's http adapters and
// app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type npm: packuments, version
//     manifests and tarballs; publish, dist-tags, deprecation and
//     unpublish; search, ping and whoami; and npm's legacy login and
//     logout.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads and writes items and writes
//     errors; the format package (artifacts/ecosystem/npm), whose index and
//     rules the answers are built from; the accounts (Options.Accounts),
//     whose password check and token service the access module provides
//     (access/api); and the remote source service (Options.RemoteSources),
//     which fetches upstream metadata and tarballs into the remote cache.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the npm error profile; DefaultMaxPublishSize and
//     DefaultLoginTokenExpiry, decision 0034's defaults.
//   - Accounts, Passwords and Tokens: what login and logout need from the
//     server.
//   - Credential, AuthLines, ScopeAuthLines and ValidScope: the .npmrc
//     lines that the root npm auth operations answer.
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
//     and npm_operations.go, which join this package later) and line 1017
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/626 (the remote
//     registry adapter) and
//     https://github.com/weaveplatform/artifactweave/issues/1031
//
// # Status
//
// Implemented P7, with remote retrieval in P9. Staged in P9d.
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
package npmhttp
