// Package condahttp serves the native Conda channel protocol at
// /artifactory/api/conda/{repoKey}: GET and HEAD of a channel's
// repodata.json, current_repodata.json and their .zst forms, notices.json,
// the opt-in CEP-16 shard index and shards, and its package archives,
// below an optional subchannel prefix. Index responses are built from
// validated package records and filtered by the caller's rights.
//
// # Role
//
// artifacts module, ecosystem layer: the native Conda channel protocol of
// the Conda slice. It is named condahttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type conda, local, federated,
//     virtual or remote, with the remote source service and the format's
//     Settings.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads items and blobs, serves
//     stored items and writes errors; the remote source service, which
//     fetches upstream repodata, shard indexes, shards and archives into
//     the remote cache; the generic download policy
//     (artifacts/ecosystem/generic); and the format package, whose rules
//     parse every path and check every document.
//
// # Outputs
//
//   - Protocol: the routes, the source of each repository class and the
//     Artifactory error profile.
//   - Answers: each index rebuilt for the caller, as JSON or zstd; an
//     empty notices.json; the shard index and shards when sharding is on
//     (404 when it is off); and package archives. repodata.json.bz2
//     answers 404.
//
// # Invariants
//
//   - An index lists only the archives the caller may read, and an archive
//     is served only when an index the caller may read lists it.
//   - A remote's repodata, shard index and shards are validated before they
//     are cached or disclosed, and every shard is pinned to the digest its
//     index names. Only a missing upstream shard index (404) falls back to
//     classic repodata, and shard and package URLs stay inside the
//     configured remote's origin and path.
//   - A remote archive must have the size and digests its index record
//     gives and be a valid Conda archive.
//   - A virtual repository combines its members' readable records in
//     member order, the first member to list a file name winning. It never
//     lends one member's rights or source permits to another, and refuses
//     a member cycle and a chain of more than eight nested repositories.
//   - Metadata responses are private and never stored by a shared cache; a
//     shard is immutable but must be revalidated.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/conda.md (connecting a client, remote and virtual
//     channels, and CEP-16 sharding).
//   - JFrog Conda repositories:
//     https://docs.jfrog.com/artifactory/docs/conda-repositories, and the
//     CEP-16 sharded repodata specification:
//     https://conda.org/learn/ceps/cep-0016/.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/conda) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/777 and
//     https://github.com/weaveplatform/artifactweave/issues/1039
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Staged in P9d.
//
// # Testing
//
//   - conda_test.go: the source and authority checks before any byte is
//     written, the archive kind lookup, the recursion and path guards, and
//     the virtual class's source.
//   - shards_test.go: the shard URL confinement and the metadata reader's
//     bounds.
//
// The routes are also tested end to end from the server root
// (conda_test.go) and from internal/app (conda_remote_test.go).
package condahttp
