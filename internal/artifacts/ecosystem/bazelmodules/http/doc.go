// Package bazelmoduleshttp serves the Bazel index registry protocol at
// /artifactory/api/bazelmodules/{repoKey}: GET and HEAD of
// bazel_registry.json, a module's metadata.json, a version's MODULE.bazel
// and source.json, its archive under archives/, and a remote version's
// patches. Every answer is derived on request from the stored or cached
// files and filtered by the caller's rights.
//
// # Role
//
// artifacts module, ecosystem layer: the native Bazel index registry
// protocol of the Bazel modules slice. It is named bazelmoduleshttp so that
// it does not shadow net/http. Outside tests, only its own http subtree,
// the module's http adapters and app may import it (decision 0044,
// dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type bazelmodules, local,
//     federated, virtual or remote, with the remote source service.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads items and blobs,
//     serves stored items and writes errors; the remote source service,
//     which fetches upstream metadata, modules, source.json documents,
//     archives and patches into the remote cache; the public base URL
//     (platform/httpkit/baseurl), which a source.json's archive URL starts
//     with; the external dependency patterns (kernel/antpath); the generic
//     download policy (artifacts/ecosystem/generic); and the format
//     package.
//
// # Outputs
//
//   - Protocol: the routes, the source of each repository class and the
//     Artifactory error profile.
//   - Answers: an empty bazel_registry.json, metadata.json, MODULE.bazel,
//     a source.json whose url is this registry's own archive URL, and the
//     archive and patch bytes.
//
// # Invariants
//
//   - A hosted version is listed and served only when the caller may read
//     its MODULE.bazel and exactly one readable archive sits beside it.
//   - A remote's metadata.json is bounded, lists unique valid versions and
//     yanks only listed ones; a remote MODULE.bazel must state the version's
//     identity; and a remote source.json must point at the configured
//     remote or a source its external dependency patterns allow, with
//     sha256 integrities for its archive and patches.
//   - A delivered source.json drops the upstream's mirrors and names this
//     registry's archive URL, so the Bazel client never fetches from
//     upstream; archives and patches are pinned to the integrities the
//     upstream source.json gives.
//   - A remote answer is revalidated against its source just before it is
//     disclosed.
//   - A virtual repository merges its members' readable versions in
//     metadata.json, and serves every other document of a version from the
//     first member that can serve all of it: a hosted member holding the
//     complete version, or a remote with a valid source.json.
//   - The download policy (#1251), after read permission: every route is
//     checked at the path 7.x keeps its file or document at in the Bazel
//     Central Registry layout, a hosted archive at
//     modules/<name>/<version>/<file> and a remote's where its cache keeps
//     it, and a refusal answers the blackout's or the patterns' 404 and
//     message, a remote's own included. A version whose files the
//     patterns reject stays listed, and its source.json is served; a
//     remote's metadata reads check its blackout alone. A virtual
//     repository leaves a blacked-out member and one whose patterns reject
//     metadata.json out of its metadata, passes over a member whose policy
//     refuses the request, and its own patterns refuse the request with "No
//     repository found to serve the request".
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/bazel-modules.md (configuring Bazel, publishing a
//     module, and remote and virtual repositories) and
//     docs/client-recipes/bazelmodules.md (the client recipe).
//   - JFrog Bazel modules repositories:
//     https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/bazelmodules) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/772,
//     https://github.com/weaveplatform/artifactweave/issues/1040 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Moved here in P9d.
//
// # Testing
//
//   - bazelmodules_test.go: the local and federated lifecycle, duplicate
//     archives, wrong manifests and corrupt archives, and, with
//     ARTIFACTWEAVE_BAZEL_CLIENT=1 and Docker, the pinned Bazel client
//     (TestAccBazelClient, which the client recipe cites).
//   - download_policy_test.go: the blackout and the patterns of local,
//     virtual and remote repositories on every route.
//   - remote_test.go, failures_test.go and fences_test.go: remote cache,
//     offline, integrity and redirects, closed failures, virtual member
//     selection and source revocation before delivery.
//   - source_test.go: the source.json origin, integrity, patch and mirror
//     policy, and metadata bounds.
package bazelmoduleshttp
