// Package nugethttp serves NuGet's protocols on NuGet repositories
// (decision 0038), mounted through the router kit (decision 0032): the v3
// API under /artifactory/api/nuget/v3/{repoKey}/ (V3) and, under
// /artifactory/api/nuget/{repoKey}/, the v2 Atom feed and the push, delete
// and relist the v3 index's PackagePublish names (V2). Every feed is built
// on each request from what the NuGet format keeps in the package index,
// for the caller and the request's base URL. Feeds need no derived
// documents, and a version is served once its deploy commits. PDB reads
// wait for derived symbol work. Downloads go through the item routes'
// download, so the download policy and counts apply to them as to any
// file, and a push stores through the item service, whose NuGet format
// checks and indexes it as it does a REST deploy.
//
// # Role
//
// artifacts module, ecosystem layer: the native NuGet v2 and v3 protocols
// of the NuGet slice, with its symbol server. It is named nugethttp so
// that it does not shadow net/http. Outside tests, only its own http
// subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts V3 and V2 through the router kit
//     (pkgroute) for repositories of package type nuget: V3's service
//     index, flat container, registration hives, search, autocomplete,
//     symbols push and PDB reads, and V2's feed, push, delete and relist,
//     for local and federated repositories and, given the remote source
//     service, remote ones.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, challenges an anonymous caller of a repository that
//     forces authentication, checks permissions, reads the package index
//     and items, stores pushes and deletes through the item service and
//     writes errors; the remote source service (the argument of V3 and
//     V2), which fetches upstream service indexes, registrations, feeds,
//     search answers, packages and PDBs into the remote cache; and the
//     format package (artifacts/ecosystem/nuget), which parses ids and
//     versions, reads packages, decodes each version's Metadata, keys PDB
//     reads and checks a remote package before it is cached.
//
// # Outputs
//
//   - V3 and V2: the routes, the source of each repository class, the
//     Artifactory error profile and the forced-authentication challenge.
//   - Answers: for a local or federated repository, the v3 documents and
//     the v2 Atom feed built from the package index, the packages, and the
//     PDBs the format extracted; for a remote repository, the upstream's
//     documents, checked and with their URLs pointed at this server for
//     this request, and its packages and PDBs from the remote cache.
//
// # Invariants
//
//   - A feed, page, count or search answer holds only what the caller may
//     read: permissions and filters apply before paging, so neither a
//     total nor an upstream offset reveals a version the caller may not
//     read.
//   - The download policy, after read permission: a blacked-out local or
//     federated repository refuses what restore reads, the flat
//     container's version list, the registration index, pages and leaves,
//     and the v2 package entry and FindPackagesById(), naming the
//     request's path, while search, autocomplete, the v2 listing and the
//     service documents keep their answers. A .nuspec takes its .nupkg's
//     check, and a symbol file the policy refuses answers the refusal
//     rather than NuGet's own 404. A remote's documents and feeds check its
//     blackout alone, so a pattern on a .nupkg refuses only the .nupkg and
//     its .nuspec and leaves the version listed.
//   - A repository with forceNugetAuthentication on answers an anonymous
//     request 401 with the Basic challenge before routing, because the
//     NuGet client sends its credentials only after a 401.
//   - Upstream documents are checked before they are cached, the URLs they
//     carry are treated as upstream data and validated, never taken from a
//     client, and a document rewritten for a caller is never stored in the
//     shared cache.
//   - Statuses and error texts stay byte-identical: dotnet, NuGet.Protocol
//     and the JFrog CLI read them.
//
// # References
//
//   - Decision 0038: docs/research/decisions/0038-nuget.md, and research 29
//     (docs/research/29-nuget.md).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers).
//   - docs/operations/nuget-remote.md (remote NuGet repositories).
//   - JFrog NuGet repositories, and the deprecation that replaces the v2
//     GetUpdates() function with search, as decision 0038 cites them:
//     https://docs.jfrog.com/artifactory/docs/nuget-repositories and
//     https://docs.jfrog.com/releases/docs/artifactory-deprecations.
//   - The NuGet server API, as decision 0038 cites it:
//     https://learn.microsoft.com/en-us/nuget/api/overview.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/nuget) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/543 to
//     https://github.com/weaveplatform/artifactweave/issues/547 (the v3 and
//     v2 protocols and their exit checks),
//     https://github.com/weaveplatform/artifactweave/issues/571
//     (PDB symbol serving), https://github.com/weaveplatform/artifactweave/issues/628
//     (remote v3), https://github.com/weaveplatform/artifactweave/issues/629
//     (remote v2 and symbols) and
//     https://github.com/weaveplatform/artifactweave/issues/1018
//
// # Status
//
// Implemented P8, with remote retrieval in P9: the v3 API (#628), then
// the v2 feed and symbols (#629). Moved here in P9d.
//
// # Testing
//
//   - v3_test.go: the service index, the flat container, restore through
//     the flat container and through the registration, registration
//     paging, hives, entries and gzip, and faults.
//   - v2_test.go: the v2 documents and exact restore, visibility and
//     filters, the client's fallback filter, paging and counts, refusals
//     and unsupported queries, store failures, and download counts.
//   - search_test.go: search and autocomplete as dotnet sends them,
//     filters, paging, faults and visible counts.
//   - push_test.go, delete_test.go and concurrency_test.go: pushes as
//     dotnet sends them, their refusals and faults, symbols pushes,
//     concurrent pushes, delete, relist, forced authentication, and write
//     transactions on PostgreSQL.
//   - remote_test.go, remote_fault_test.go, remote_admission_test.go and
//     remote_inline_test.go: remote v3 discovery, paging, permissions and
//     identity, gzip and offline reads, the admission of upstream
//     documents, tampering, refresh, private credentials and redirects,
//     repository signatures, and the final disclosed-path fence.
//   - remote_v2_test.go, remote_v2_fault_test.go and
//     remote_v2_dependencies_test.go: remote v2 paging, queries, downloads
//     and offline reads, snapshot retention, XML admission, redirects,
//     credentials and no-store answers, and dependency groups.
//   - remote_symbols_test.go: remote PDB bytes, ranges, caching,
//     permissions and refusals.
//   - policy_test.go: the download policy on local restore reads, symbols,
//     search and listings, and on remote v3 documents and v2 feeds.
//   - fixture_test.go holds the fixture the tests share, with the access
//     test kit (access/accesstest) for permissions.
//   - The guard suite test/server/artifacts/nuget and the @nuget acceptance
//     scenarios (test/acceptance/features/nuget_*.feature) drive the
//     protocols through the whole server.
package nugethttp
