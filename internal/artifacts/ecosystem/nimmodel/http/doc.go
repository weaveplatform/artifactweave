// Package nimmodelhttp serves the documented NGC metadata and
// model-resource URLs through a remote NIM repository. Container layers use
// the separate Docker remote and its own credentials and permissions.
//
// # Role
//
// artifacts module, ecosystem layer: the NVIDIA NIM model protocol, at
// /artifactory/api/nimmodel/{repoKey}/ through the router kit (decision
// 0032). NIM is remote only and pairs with a Docker remote, which serves
// the matching container image, so the slice has no format package. It is
// named nimmodelhttp so that it does not shadow net/http. Outside tests,
// only its own http subtree, the module's http adapters and app may import
// it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type nimmodel: GET and HEAD of
//     NVIDIA's tag metadata (v2/nim/metadata, as a query or as a path),
//     repository metadata (v2/repos and v2/org/...) and versioned model
//     files and ZIPs (v2/models and v2/org/.../models); and the
//     application's remote policy, which calls PrepareUpload for every
//     upload into a NIM repository's cache.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, challenges every anonymous caller, checks permissions
//     on the path each resource names and serves results and errors; and
//     the remote source service (the argument of Protocol), which fetches
//     each resource from NGC into the remote cache and refreshes it or
//     serves it offline under the P9 remote policy.
//
// # Outputs
//
//   - Protocol: the routes, the remote source and the Artifactory error
//     profile. Any other repository class answers 501 (decision 0032);
//     repository validation admits none.
//   - The cache path of each resource: tag metadata at
//     models/<id>/<tag>/.jfrog_nim_model_info.json, repository metadata at
//     catalog/<id>/repo.json and model files and ZIPs under
//     models/<id>/<version>/.
//   - PrepareUpload: publishes validated NIM metadata with the
//     Nimmodel.name and Nimmodel.versionId properties, by which
//     Artifactory's property search finds a model.
//
// # Invariants
//
//   - Each request identity has a cache path of its own, so a change of
//     tag, repository or file never reuses another's representation.
//   - The route is no general proxy: a path segment that is encoded, empty,
//     a dot segment or outside the safe characters answers 400 before
//     anything is fetched, and a resource NVIDIA does not document 404.
//   - Metadata is bounded (16 MiB) and must be one JSON object, and tag
//     metadata must give each profile an id and a list of tested GPU
//     devices, before it is cached: a malformed or truncated answer never
//     publishes an entry.
//   - The properties come from the request's path, never from fields the
//     upstream supplies.
//   - The route never serves image layers: the paired Docker remote checks
//     their digests and its own grant.
//
// # References
//
//   - docs/testing/nimmodel.md (NIM remote qualification).
//   - Research 33: docs/research/33-package-formats.md, "NVIDIA NIM".
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit); decision 0003:
//     docs/research/decisions/0003-repository-model.md (repository classes);
//     decision 0031: docs/research/decisions/0031-docker-oci-registry.md,
//     and docs/operations/registry-remote.md (the paired Docker remote).
//   - JFrog NVIDIA NIM repositories:
//     https://docs.jfrog.com/artifactory/docs/nvidia-nim-repositories.
//   - NVIDIA's metadata API and model file URLs:
//     https://docs.nvidia.com/nim/metadata-api/reference/endpoints and
//     https://docs.nvidia.com/ngc/latest/ngc-catalog-user-guide.html#downloading-models-via-wget-curl.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 384 (remote only, paired with a Docker remote), lines 894 to 897
//     (thin http slices as a coverage risk) and line 1025 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/687 (the NIM
//     remote), https://github.com/weaveplatform/artifactweave/issues/693
//     (qualification evidence),
//     https://github.com/weaveplatform/artifactweave/issues/216 (the wire
//     shape still inferred) and
//     https://github.com/weaveplatform/artifactweave/issues/1022
//
// # Status
//
// Implemented P9b: NVIDIA's documented paths, qualified against a
// controlled fixture. The Artifactory wire shape, the other .jfrog cache
// files and the remaining Nimmodel.* properties stay inferred for P19's
// comparison (#216). Moved here in P9d.
//
// # Testing
//
//   - nimmodel_test.go: PrepareUpload's properties, the documented NGC
//     paths, unscoped and unknown requests, and tag metadata admission.
//   - remote_test.go: cold, warm, offline and refreshed metadata,
//     repository discovery, denials and invalid upstream answers that
//     publish nothing, the model file cache and its faults, and the
//     separate authority of the paired Docker registry.
//   - The @nimmodel acceptance scenario drives a controlled NGC answer and
//     a native crane pull through the paired Docker remote.
package nimmodelhttp
