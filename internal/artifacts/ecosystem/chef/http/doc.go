// Package chefhttp serves the Chef Supermarket cookbook protocol at
// /artifactory/api/chef/{repoKey}/api/v1: the catalog and search, cookbook
// and version records, version archives, publishing by knife-art's raw
// upload or the Supermarket multipart form, and deletes. Every catalog and
// record lists only the versions the caller may read, with links that name
// the serving repository, so Knife resolves dependencies behind its
// permissions. Archive validation and the cookbook rules are the format
// package's (artifacts/ecosystem/chef); this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Chef Supermarket protocol of
// the Chef slice. It is named chefhttp so that it does not shadow net/http.
// Outside tests, only its own http subtree, the module's http adapters and app
// may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type chef, local, federated,
//     virtual or remote. The server also calls ChefAPI's
//     RegisterChefReindex, which serves the repository's reindex (POST
//     {repoKey}/reindex) beside the protocol, and lists the artifacts
//     module's TryChefItem (artifacts/adapters/http) among the item kit's
//     overlays, which keeps the generic item routes from serving a remote's
//     cached metadata.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads the package index, items and
//     blobs, stores a published archive, deletes versions and writes errors;
//     the remote source service (Protocol's argument), which fetches an
//     upstream Supermarket's catalog, cookbook and version documents and its
//     archives into the remote cache; and the format package, whose rules
//     check every name, version, path, document and archive. ChefAPI's
//     reindex is the item kit's Reindex (artifacts/adapters/http/itemkit),
//     which every format shares.
//
// # Outputs
//
//   - Protocol: the routes, the source of each repository class and the
//     Artifactory error profile.
//   - ChefAPI: the kit wrapper whose RegisterChefReindex mounts POST
//     /artifactory/api/chef/{repoKey}/reindex, which refuses any query with
//     400, answers its text once the reindex has run, and writes errors in
//     the Artifactory envelope.
//   - Answers: catalogs paged by start and items, at most 1,000 entries a
//     page; cookbook and version records whose links name the serving
//     repository; archives; the version record of a published cookbook; and
//     the name and version a delete removed. Every JSON answer is marked
//     private, no-store.
//
// # Invariants
//
//   - A version is listed, and latest resolved, only among the versions the
//     caller may read and the repository's blackout and include and exclude
//     patterns allow. A hosted version whose stored index entry or archive
//     does not match fails with 502 rather than being advertised.
//   - A hosted or virtual catalog, and a cookbook's versions, hold at most
//     4,096 entries (503 beyond); a remote accepts no more from its
//     upstream.
//   - A published version is immutable: publishing it again answers 409.
//   - A remote builds its own paging queries, reads at most 4 MiB a
//     document, and keeps metadata and archive links under the configured
//     Supermarket origin and path. An archive enters the cache only once its
//     size, its SHA-256 where the version declares one, its identity and its
//     dependencies and platforms match the version that advertised it. A
//     cache hit rechecks the source's authority and the caller's read
//     permission.
//   - A virtual repository resolves its members in order, the first member
//     that holds a version supplying it.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/chef.md (connecting Knife and knife-art, the protocol and
//     lifecycle, remote and virtual repositories, and the evidence), and the
//     Chef Supermarket API: https://docs.chef.io/supermarket/supermarket_api/.
//   - JFrog Chef repositories:
//     https://docs.jfrog.com/artifactory/docs/chef-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/chef) and line 1017 (the mapping row). The
//     server-root table does not list the root's chef_routes.go, which
//     joined this package as routes.go.
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epics ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943) and WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/783,
//     https://github.com/weaveplatform/artifactweave/issues/1045 and
//     https://github.com/weaveplatform/artifactweave/issues/1086
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d, which also moved the server root's reindex route
// here (WEB-17).
//
// # Testing
//
//   - chef_test.go: hosted publish, resolve, delete and reindex, multipart
//     publish and whole-cookbook delete, invalid requests, remote
//     validation, authority, pagination and caching, virtual order and
//     permission filtering, and broken stored indexes.
//   - metadata_test.go: upstream version identity and URL boundaries,
//     catalog bounds, version order and empty dependency objects.
package chefhttp
