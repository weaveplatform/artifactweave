// Package dockerhttp serves the registry protocol, the OCI Distribution
// Specification v1.1 with Docker's extensions, for docker, oci and helmoci
// repositories (decisions 0003 and 0031). It answers three addressing forms:
//
//   - the repository path, /v2/<repo>/<image>/..., the native default, which
//     needs no reverse proxy;
//   - Artifactory's own prefix, /artifactory/api/docker/<repo>/v2/<image>/...;
//   - the subdomain, <repo>.<base_url host>/v2/<image>/..., when
//     docker.subdomain_match is on.
//
// The router is hand-written and mounted before the generated servers
// (decision 0002). It parses each path into a Request and hands it to the
// endpoint a feature registered for its kind and method; an operation the
// specification defines that no feature has registered yet answers 405
// UNSUPPORTED. Beside the registry, the Docker partial strict server
// (DockerAPI) answers the JFrog REST operations under a repository's
// Docker API path: tag delete, promotion and parent manifest lists.
//
// # Role
//
// artifacts module, ecosystem layer: the registry protocol of the registry
// slice, /v2 with registry tokens and subdomain routing, one protocol for the
// package types docker, oci and helmoci. It is named dockerhttp so that it
// does not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency rule
// 9).
//
// # Inputs
//
//   - Driving: the server, which builds the Registry with New and mounts it
//     with Register before the generated servers, runs APIVersion and
//     Challenge as middleware, and asks IsRegistryPath and
//     ChallengesAnonymous while it authenticates, normalises content types
//     and answers unimplemented operations. The router also embeds
//     DockerAPI in the artifactory spec's strict server; its operations
//     (rest.go) check image names with ValidName and find repositories with
//     Lookup.
//   - Driven: the registry's store (Options.Store, an
//     artifacts/core/registry Store), which resolves repositories and reads
//     items, and over which the registry's writes (artifacts/core/registry)
//     keep upload sessions and store blobs, mounts, manifests and deletes
//     through the item service; Lookup reads only repositories, from any
//     format.Store; the format package (artifacts/ecosystem/docker, imported
//     as layout), which names paths, parses manifests and finds blobs,
//     manifests, tags, images and referrers; the access module's API
//     (access/api), whose decider each request carries, whose Tokens sign
//     registry tokens (TokenIssuer) and whose Identity reads a user's email;
//     the registry's remote source service (Options.Sources), through which
//     a remote repository's manifests, blobs, tags and referrers are read;
//     and the server's download (Options.ServeBlob,
//     Options.RedirectManifest), which applies the download redirect policy
//     (decision 0029) and counts downloads. DockerAPI runs over the item kit
//     (artifacts/adapters/http/itemkit), which resolves the caller's access
//     and writes errors, and over its store (itemkit.Store), in which it
//     finds repositories and over which it builds the registry service its
//     deletes and promotions go through.
//
// # Outputs
//
//   - Registry: New with Options, Register, the Challenge middleware, and
//     the Endpoint, Request and Kind its features register and serve.
//   - DockerAPI: the Docker partial strict server, whose three operations
//     (DeleteDockerTag, PromoteDockerImage, FindParentManifestLists) answer
//     in the Artifactory envelope.
//   - Middleware and predicates for the server: APIVersion,
//     IsRegistryPath, ChallengesAnonymous, GrantsCatalog, ValidName,
//     Serves and Lookup.
//   - Errors in the specification's envelope: Errorf, Write and WriteAll,
//     with the Code constants; and the headers APIVersionHeader and
//     RegistryHeader.
//   - Answers: the probe, the token endpoint, and the docker config.json
//     entries at .../v2/auth and the legacy .../v1/auth; for a local
//     repository, blob reads, monolithic and chunked uploads and
//     cross-repository mounts, manifest and index pushes, reads and
//     deletes, tags lists, catalogs and referrers; for a remote repository,
//     reads of manifests, blobs, tags and referrers through its cache.
//
// # Invariants
//
//   - This package never imports the router (internal/app) that mounts it
//     (decision 0031).
//   - Every registry response carries Docker-Distribution-API-Version, the
//     401 included, and every 401 under a registry path carries the Bearer
//     challenge Docker clients follow to the token endpoint (decision
//     0005). A request without credentials is challenged on every registry
//     path but the token endpoint, even while anonymous access is on.
//   - On a server that authenticates every request (Options.Authenticated),
//     a request whose principal carries no decider is refused, never
//     allowed.
//   - A remote repository answers reads only, and a virtual one 405
//     UNSUPPORTED.
//   - The JFrog operations under the Artifactory prefix (promote,
//     parentManifests and delete) are no registry paths and keep the
//     Artifactory envelope.
//   - Codes, statuses and messages stay byte-identical: docker, crane,
//     oras, cosign, helm and the JFrog CLI read them.
//   - A read the download policy refuses, a local or a remote repository's,
//     answers 404 with the policy's message in Artifactory's envelope,
//     under no registry code, as the Docker client showed 7.x's answer
//     (RTFACT-27234); a missing manifest or blob keeps its registry code.
//     Blackout is checked before the item is looked for, the patterns on
//     the copy a pull serves, and a blob is served from any copy in the
//     image the patterns accept (RTFACT-30134). A blacked-out repository
//     also refuses an image's tags list and referrers, naming the image as
//     a remote's refusal does; the patterns refuse neither. The catalog
//     leaves out an image its repository's patterns reject (RTFACT-26524),
//     and a mount from another repository that would refuse the blob opens
//     a session.
//   - A remote repository's failure to fetch, its upstream's 401, 403, 429
//     or 5xx or an upstream that is unreachable, unusable or slow, answers
//     its status and message in Artifactory's envelope, under no registry
//     code, as the Docker client showed 7.x's answer to an upstream that
//     refused its credentials (RTFACT-30467, #1248). The caller's own
//     refusal keeps DENIED, or UNAUTHORIZED when anonymous.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md
//     (protocol and storage layout), and research 23
//     (docs/research/23-docker-oci-registry.md).
//   - Decision 0003: docs/research/decisions/0003-repository-model.md (the
//     addressing forms); decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md (the
//     registry token); decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md;
//     decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md;
//     decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks).
//   - Research 17: docs/research/17-undocumented-shapes.md (the token
//     endpoint's values).
//   - docs/operations/registry-remote.md (Docker, OCI and Helm OCI remote
//     repositories) and docs/testing/nimmodel.md (the Docker remote a NIM
//     repository pairs with).
//   - The OCI Distribution Specification v1.1.1:
//     https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md.
//   - JFrog's pages decision 0031 cites:
//     https://docs.jfrog.com/artifactory/docs/docker-repositories and
//     https://docs.jfrog.com/artifactory/docs/additional-docker-information.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 229 (ecosystem/docker; http/ holds /v2), line 404 (the root's
//     docker_rest.go, now rest.go) and line 1022 (the mapping row).
//   - Plan: docs/implementation/plan.md#p6-dockeroci,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epics ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943) and WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/220 (routing),
//     https://github.com/weaveplatform/artifactweave/issues/221 (tokens),
//     https://github.com/weaveplatform/artifactweave/issues/222 (blobs),
//     https://github.com/weaveplatform/artifactweave/issues/223 and
//     https://github.com/weaveplatform/artifactweave/issues/225 (manifests,
//     indexes and referrers),
//     https://github.com/weaveplatform/artifactweave/issues/226 (listings and
//     deletes), https://github.com/weaveplatform/artifactweave/issues/624
//     (remote reads), https://github.com/weaveplatform/artifactweave/issues/1022,
//     https://github.com/weaveplatform/artifactweave/issues/1086,
//     https://github.com/weaveplatform/artifactweave/issues/1089 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P6, with remote retrieval in P9. Moved here in P9d, which also
// moved the server root's Docker REST operations here (WEB-17).
//
// # Testing
//
//   - docker_test.go: path parsing, image names, registry paths and
//     anonymous challenges, the version header, routing, subdomains, the
//     JFrog operations under the prefix, the probe, store failures, Require,
//     and the refusal of a request without a decider on an authenticated
//     server.
//   - blobs_test.go: monolithic and chunked uploads, pulls, mounts,
//     permissions, absolute locations, the ServeBlob hook, streaming layers,
//     and store and index failures.
//   - manifests_test.go and referrers_test.go: pushes and pulls by tag and
//     digest, refusals, body limits, permissions and store failures;
//     indexes and the referrers API.
//   - tags_test.go: tags lists and pagination, catalogs, deletes, their
//     permissions and failures, and the catalog grant.
//   - token_test.go: the token endpoint, its grants, lifetime and parent
//     binding, the challenge and the auth endpoints, on the access test kit
//     (access/accesstest).
//   - pull_policy_test.go, reject_test.go and remote_listing_test.go: the
//     download policy's refusals of pulls, of tags lists and of referrers,
//     a blob served from a copy the patterns accept, mounts from a refusing
//     source and catalogs the patterns filter; a format's refusals of writes,
//     which keep their status; and a remote listing's final fence.
//   - remote_error_test.go: the envelope and code of each failure a remote
//     read answers, and kernel's shared errors left unchanged.
//   - The guard suites test/server/artifacts/docker and
//     test/server/artifacts/helmoci, and the OCI conformance job, drive the
//     registry through the whole server; the docker suite drives DockerAPI's
//     operations too, which have no tests here, as the root file had none.
package dockerhttp
