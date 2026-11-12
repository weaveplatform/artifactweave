// Package helmhttp serves classic Helm repositories under /api/helm/: at
// /artifactory/api/helm/{repoKey}/, through the router kit (decision 0032),
// the root index.yaml, the charts and their provenance files that helm repo
// add, helm repo update and helm pull read. A chart is published through
// the item routes' deploy, which the Helm format indexes as it indexes any
// write, so the protocol only reads (decision 0039).
//
// # Role
//
// artifacts module, ecosystem layer: the native classic Helm repository
// protocol of the Helm slice. It is named helmhttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type helm: GET and HEAD of the
//     root index.yaml and of any other path. The reindex operations under
//     the same prefix are the server's generated operations, which Protocol
//     reserves.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, serves items and writes errors;
//     the format package (artifacts/ecosystem/helm), whose derived index and
//     chart rules the answers are built from; and the remote source service
//     (Options.RemoteSources), which fetches the upstream index.yaml, charts
//     and provenance files into the remote cache.
//   - Settings: Options.Format, the server's Helm options, which choose
//     relative or absolute chart URLs.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: for a local or federated repository, the derived index.yaml
//     once the work its writes queued has run, with the stored relative
//     chart URLs or, when Settings turn them off, absolute ones against the
//     request's base URL, and every other file as the item routes serve it;
//     for a remote repository, the upstream index with its chart URLs
//     rewritten to this repository and its other fields kept, and charts
//     and provenance files checked against it before they are cached or
//     served.
//
// # Invariants
//
//   - The remote index.yaml is cached as the upstream sent it; an answer
//     rewrites only its chart locations, lists only the charts the caller
//     may read, and keeps signed chart and provenance bytes unchanged.
//   - A provenance file is served only while it matches the current index,
//     offline reads included.
//   - The download policy (#1251) refuses index.yaml, local or remote, at
//     its own path, where 7.x stores or caches it: a blacked-out repository
//     or a pattern on index.yaml answers 404 naming it, after Read
//     permission. A chart the patterns reject stays listed, and only its
//     download and a provenance file the patterns reject are refused, each
//     at its own path. A remote chart download's index lookup is refused
//     by the blackout alone, naming the requested file.
//   - Statuses, error texts and the remote cache variants (helm-chart-v2,
//     helm-provenance-v1) stay byte-identical.
//
// # References
//
//   - Decision 0039: docs/research/decisions/0039-helm.md ("URLs in
//     index.yaml", "Routes under /artifactory/api/helm/{repoKey}/", "Local
//     permissions", "P9 shared retrieval contract").
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit).
//   - docs/operations/helm-remote.md (remote repositories) and
//     docs/implementation/remote-retrieval-contract.md.
//   - JFrog Helm chart repositories, as docs/operations/helm-remote.md cites
//     it: https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories;
//     and the metadata read rule that decision 0039 cites:
//     https://docs.jfrog.com/artifactory/docs/docker-repositories.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/helm) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/630 (classic
//     remote reads) and
//     https://github.com/weaveplatform/artifactweave/issues/1033
//
// # Status
//
// Implemented P8, with remote retrieval in P9. Moved here in P9d.
//
// # Testing
//
//   - helm_test.go: repo add and pull, cold index reads, refusals and index
//     failures through a server.
//   - remote_test.go, remote_validation_test.go, remote_provenance_test.go
//     and remote_review_regression_test.go: remote indexes, charts and
//     provenance, URL policy, admission limits, offline reads and
//     disclosure.
//   - download_policy_test.go: the download policy at the local and remote
//     index.yaml, charts and provenance files.
package helmhttp
