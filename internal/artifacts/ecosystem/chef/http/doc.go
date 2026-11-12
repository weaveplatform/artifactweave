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
//   - Answers: catalogs and searches paged by start and items, at most
//     1,000 entries a page, a larger items, such as Knife's 9999999, clamped
//     to that, and Knife's order and user taken and ignored (#1305); a
//     search looks in each cookbook's name, description and maintainer, and
//     a remote's, or a virtual repository's remote members', is forwarded
//     to the upstream's search and merged after the cookbooks the cache or
//     hosted members hold (JFrog, "Search for Chef Cookbooks"); cookbook
//     and version records whose links name the serving
//     repository; archives; the version record of a published cookbook; and
//     the name and version a delete removed. Every JSON answer is marked
//     private, no-store.
//
// # Invariants
//
//   - A version is listed, and latest resolved, only among the versions the
//     caller may read. A hosted version whose stored index entry or archive
//     does not match fails with 502 rather than being advertised.
//   - The download policy (#1251), after read permission: a blacked-out
//     repository refuses the cookbook and version records before they are
//     built, with the blackout's 404 naming the request's path, while its
//     catalog and search keep their answers (empty, or a remote's blackout
//     refusal); the patterns do not refuse them, and a version whose
//     archive they reject stays listed, latest included. A download is
//     refused at the archive's path, <name>/<name>-<version>.tar.gz, with
//     the policy's 404 and message, a hosted or virtual repository's
//     whether the named version exists or not. A remote's metadata reads
//     check its blackout alone, and its archive read its patterns as well.
//     A virtual repository leaves a blacked-out member out, and its own
//     patterns refuse a download with "No repository found to serve the
//     request".
//   - A hosted catalog, the cookbooks a virtual repository's members index,
//     and a cookbook's versions hold at most 4,096 entries (503 beyond), and
//     a remote accepts no more versions of a cookbook from its upstream. An
//     upstream's catalog has no such bound: a remote's catalog, and a
//     virtual repository's with a remote member, take a start up to 2^30
//     and an upstream total up to that (#1306).
//   - A published version is immutable: publishing it again answers 409.
//   - A remote builds its own paging queries: it asks its upstream for 100
//     cookbooks a page, as many as Supermarket serves. A catalog page reads
//     only the upstream pages that cover its offsets, from the 100-aligned
//     page that holds start, at most 11 for a page of 1,000, and never a
//     cookbook or version record (#1306); a search forwards its query once.
//     A catalog page with more after it that holds fewer than 100 fails
//     with 502, since Supermarket's page states no limit that would explain
//     it, and so does any page of more than 1,000 (#1298).
//   - A remote reads at most 4 MiB a document, and keeps metadata and
//     archive links under the configured Supermarket origin and path. An
//     archive enters the cache only once its size, its SHA-256 where the
//     version declares one, its identity and its dependencies and platforms
//     match the version that advertised it. A cache hit rechecks the
//     source's authority and the caller's read permission.
//   - A virtual repository resolves its members in order, the first member
//     that holds a version supplying it; a download passes over a member
//     whose own policy refuses the archive to the next one that holds the
//     version.
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
//   - JFrog, "Search for Chef Cookbooks", in the 7.x Chef Cookbook
//     Repositories guide: how local, remote and virtual repositories
//     answer knife search.
//   - https://github.com/weaveplatform/artifactweave/issues/783,
//     https://github.com/weaveplatform/artifactweave/issues/1045,
//     https://github.com/weaveplatform/artifactweave/issues/1086,
//     https://github.com/weaveplatform/artifactweave/issues/1305 and
//     https://github.com/weaveplatform/artifactweave/issues/1306
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d, which also moved the server root's reindex route
// here (WEB-17).
//
// # Testing
//
//   - catalog_test.go: Knife's list and search queries on every class, the
//     fields a search looks in, a remote's search merged after its cache,
//     and a virtual catalog that lists hosted cookbooks, then an
//     upstream's.
//   - chef_test.go: hosted publish, resolve, delete and reindex, multipart
//     publish and whole-cookbook delete, invalid requests, remote
//     validation, authority, pagination and caching, virtual order and
//     permission filtering, and broken stored indexes.
//   - download_policy_test.go: the blackout and the patterns of local,
//     virtual and remote repositories on every route, remotes read through
//     the source boundary from a fake upstream Supermarket.
//   - metadata_test.go: upstream version identity and URL boundaries,
//     catalog bounds, version order and empty dependency objects.
//   - remote_paging_test.go: the upstream catalog and search pages a remote
//     accepts and refuses.
package chefhttp
