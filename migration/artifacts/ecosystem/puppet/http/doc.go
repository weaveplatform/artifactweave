// Package puppethttp serves the Forge v3 module protocol at
// /artifactory/api/puppet/{repoKey}: a module's releases, one release and
// its archive, a module record and an exact-name search, in the JSON the
// Puppet client's Forge client reads. Every answer lists only the releases
// the caller may read, so the client resolves dependencies behind the
// repository's permissions. Archive validation and the module rules are the
// format package's (artifacts/ecosystem/puppet); this package answers HTTP
// with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Puppet Forge v3 protocol of
// the Puppet slice. It is named puppethttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type puppet, local, federated,
//     virtual or remote. The root file puppet_routes.go mounts it again
//     behind the generated Forge operations and serves the repository's
//     reindex (POST {repoKey}/reindex) beside it. An archive is published
//     through the generic item routes, not through this protocol; the
//     format's Handler admits it there.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads the package index, items
//     and blobs, and writes errors; the remote source service
//     (Options.RemoteSources), which fetches an upstream Forge's release
//     pages and archives into the remote cache; the blob store
//     (Options.Blobs), which opens cached release pages; and the format
//     package, whose rules check every name, version, path, manifest and
//     archive.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository class
//     and the Artifactory error profile.
//   - Answers: release lists with Forge pagination, at most 1,000 releases
//     a page; release and module records; search results; and archives.
//     Every JSON answer is marked private, no-store.
//
// # Invariants
//
//   - A release is listed only when the caller may read its archive and the
//     repository's blackout and include and exclude patterns allow it. A
//     hosted release whose stored index entry or archive does not match
//     fails with 502 rather than being advertised.
//   - A hosted or virtual module lists at most 4,096 releases (503 beyond),
//     and a remote accepts no more from its upstream.
//   - A remote builds each Forge query itself (module, limit, offset and
//     sort_by), never follows an upstream next link, and reads at most
//     16 MiB a page. Metadata and archive URLs must stay under the
//     configured Forge origin and path, and an archive enters the cache only
//     once its size, SHA-256, identity and manifest match the release that
//     advertised it. A cache hit rechecks the source's authority and the
//     caller's read permission.
//   - A virtual repository resolves its members in order, the first member
//     that holds a version supplying it, and each archive download selects
//     that member again.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/puppet.md (connecting the Puppet client, remote and
//     virtual repositories, the remote's bounds and the evidence).
//   - JFrog Puppet repositories:
//     https://docs.jfrog.com/artifactory/docs/puppet-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/puppet) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/784 and
//     https://github.com/weaveplatform/artifactweave/issues/1045
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - puppet_test.go: hosted routes, invalid routes and queries, remote
//     authority, integrity, pagination and failures, virtual member order,
//     permissions and changes, store failures and late read revocation.
//   - remote_test.go: upstream document validation, the archive URL and
//     feed boundaries, version order, and a remote archive refused before
//     its source when its authority is missing.
package puppethttp
