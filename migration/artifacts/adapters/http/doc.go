// Package artifactshttp is the artifacts module's management REST: the
// ArtifactoryAPI partial of the artifactory spec's strict server (the
// repository, build, search, trash, storage summary and reindex
// operations) and the hand-written routes beside it (items, storage,
// trash, multipart uploads, transfer, the searches and AQL), with the item
// overlays and private paths of the formats whose files it serves, the
// package protocol list and the idempotency middleware. The router builds
// the partial from Deps with New and mounts its registrars in a fixed
// order.
//
// # Role
//
// artifacts module, HTTP adapter layer (management REST). The composition
// root mounts what this package exports beside the other partials. The
// package imports no server or router package: the router hands it what it
// needs as Deps. It embeds the item kit (itemkit), which the ecosystem
// partials share, and builds the ecosystem HTTP packages' protocols
// (Protocols). It reaches the access module only through access/api and the
// kernel's principal helpers, and the configuration module not at all: the
// descriptor's minimum redirect size reaches the kit as a function in its
// Deps.
//
// # Inputs
//
//   - Driving: the router, which embeds ArtifactoryAPI in the generated
//     strict server beside the other partials and the stubs, mounts the
//     registrars, SmartStatus and Protocols, and installs IdempotencyRoutes
//     and HelmPartialReindex as middleware.
//   - Deps, from the composition root: the item kit's dependencies (the
//     stores, the remote sources, the work queue, the formats and their
//     settings, the access module's API, the redirect policy, the logger,
//     the overlays and the private predicate) and the handlers' own: the
//     remote connection pools, the remote status diagnostics, the garbage
//     collector, the multipart upload service, the blob mirrors and
//     location, the AQL and search limits, and the build, folder download,
//     Hugging Face and npm settings.
//   - The request's principal and decider in its context, which the
//     authentication middleware put there, and the raw query and body that
//     the router's request details keep for the operations that read them
//     (KeepsSearchQuery, KeepsRepositoryBody, KeepsBuildBody).
//
// # Outputs
//
//   - ArtifactoryAPI, built by New: the strict operations of its slice of
//     the artifactory spec; the registrars RegisterItemRoutes,
//     RegisterTransferRoutes, RegisterStorageRoutes, RegisterTrashRoutes,
//     RegisterMultipartRoutes and RegisterSearchRoutes; SmartStatus;
//     HelmPartialReindex; and ProtocolOptions, the router kit's options for
//     every protocol.
//   - The router's contributions: IdempotencyRoutes, SelfAuthenticated,
//     SlashCollections, KeepsSearchQuery, KeepsRepositoryBody,
//     KeepsBuildBody, PrivateItem, and the overlays TryCondaItem,
//     TryMachineLearningItem and TryChefItem.
//   - Protocols: the package protocols with routers of their own.
//   - The configuration types AQLLimits, SearchLimits, Mirrors and
//     MirrorStatus, which the router's Config and the application name.
//
// # Invariants
//
//   - Registration order: the router mounts the item routes (with AQL)
//     first, then transfer, storage, trash, multipart and the searches, all
//     ahead of its hand-written stubs, so that each route claims the pattern
//     the spec derives and replaces its 501 stub rather than sitting beside
//     it (decision 0002). HelmPartialReindex is middleware that runs after
//     authentication and ahead of routing, which cannot match its path.
//   - Private paths are never served through the generic routes: the
//     overlays answer the paths PrivateItem names 404 on an item GET, a
//     checksum deploy never reuses their content, and trash and transfer
//     relocate or skip them by each format's own rule.
//   - Statuses, envelopes and error, log and metric text are what JFrog's
//     clients and the operators' logs read, so they stay byte-identical.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     (hand-written item paths mounted ahead of the generated routes).
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the item model and content-addressed blobs).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the trash can and garbage collection).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md (the
//     searches and AQL).
//   - Decision 0014: docs/research/decisions/0014-build-info.md (build-info).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     (the download redirect).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md (retry
//     safety, idempotency keys and bounded execution).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 238 to 246 (artifacts/adapters/http).
//   - https://github.com/weaveplatform/artifactweave/issues/1078
//
// # Status
//
// Implemented P1 to P9c as the server root's management handlers; P9d made
// them the ArtifactoryAPI partial (WEB-08) and moved them here (WEB-12).
// The GET searches' logic is in artifacts/core/search, where ART-17 also
// moves the upstream search.
//
// # Testing
//
// The tests here call the partial and its helpers without the router, over
// in-memory stores and the access module of accesstest, which the package's
// test fixture binds as the router would: the storage summary and its
// formatting, the storage view's permissions, the repository operations,
// the idempotency keys and replays, the transitive AQL peer rows and
// bounds, the AQL timeouts behind a router of the item routes alone, and
// the upstream search disclosures. The route suites under
// test/server/artifacts reach the rest through the router.
package artifactshttp
