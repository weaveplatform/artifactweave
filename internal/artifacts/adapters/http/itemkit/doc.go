// Package itemkit is the item kit: the item helpers that the artifacts
// management handlers and the ecosystem HTTP handlers both call, in one
// package that both can import. It resolves repositories and callers, looks
// items up through local, remote and virtual repositories, serves files
// through the download redirect, reads remote repositories through the source
// boundary, queues and awaits reindexes, and maps the item service's errors
// onto statuses. The composition builds a Kit from Deps and supplies the
// format overlays and the private-path predicate.
//
// # Role
//
// artifacts module, HTTP adapter layer: the shared item kit. The artifacts
// HTTP adapter, the ecosystem HTTP packages (ecosystem/<type>/http) and the
// router in app embed or call it. It never imports app or an ecosystem HTTP
// package. It reaches the access module only through access/api, and the
// configuration module not at all: the descriptor's minimum redirect size
// reaches it as a function in Deps.
//
// # Inputs
//
//   - Deps: the metadata store, through Store: the item service's store
//     (item.Store), in which the kit resolves repositories, reads items and
//     the remote cache and queues work, and over which it builds the item
//     service, with the upload sessions that the Docker REST operations'
//     registry service keeps. Then the blob store, the remote sources, the
//     work queue, the formats and their settings, the access module's API,
//     the redirect policy and the descriptor's minimum size, the logger,
//     the overlays and the private-path predicate.
//   - The request's principal and decider in its context, which the
//     authentication middleware put there.
//
// # Outputs
//
//   - Store, the store Deps.Store holds, which Content returns and each
//     Overlay is handed. It names no access or configuration-module store.
//   - Kit and its methods: GetItem (an item GET or HEAD), ServeFile,
//     RedirectManifest, Items (the item service), Format, ItemCaller,
//     ItemError, MayBeIdentical, Content, InternalError, Log, Reindex,
//     ReindexAs, OperationRepo, Enqueue, Queue, Await, ServeRemoteStream and
//     RemoteDeliveryAllowed.
//   - Item values and helpers: ItemRequest, ParseItemPath,
//     ParseEscapedItemPath, Found, Download, Lookup, StatusWriter,
//     ResolveRepo, DeployTarget, ItemCreated, ItemNotFound,
//     OverwriteRefusal, ChecksumsJSON, DownloadURI, SplitRepoPath,
//     NotFoundRepo and View.
//   - Access, Target and AccessFor: the questions handlers ask the
//     request's permission decider.
//   - The per-format remote source requests (AlpineSourceRequest and the
//     others), RemoteResource, and ReindexOps, the reindex operations by
//     package type.
//
// # Invariants
//
//   - GetItem tries Deps.Overlays in order and stops at the first that
//     answers. A format's route takes precedence over the generic lookup and
//     over every overlay after it, so the composition lists them in one
//     fixed dispatch order: agent marketplace, Alpine, Conda, machine
//     learning, Chef, CRAN, Debian, RPM, opkg, P2.
//   - Deps.Private names the paths whose content a checksum deploy may
//     never reuse. Trash and transfer test the format predicates themselves,
//     because each format relocates and archives its private paths by its
//     own rule.
//   - Items are looked up, downloaded and deployed as before the kit was
//     split out: the same routes, statuses, error texts and log lines.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the item model and content-addressed blobs).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote delivery through the source boundary).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     (the redirect policy and its minimum size).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes before the generic lookup).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission checks).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 238 to 246 (artifacts/adapters/http).
//   - https://github.com/weaveplatform/artifactweave/issues/1075 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P1 to P9c as the server root's item routes and helpers;
// split out as the item kit in P9d (WEB-06), which also narrowed its store
// to Store (ART-22).
//
// # Testing
//
// kit_test.go tests the kit over the in-memory world of kittest, without a
// router: the overlay order, the private-path default, the error mapping and
// the lookup through a virtual repository. access_test.go tests AccessFor, and
// redirect_test.go the signing-failure throttle and the descriptor's minimum
// redirect size. The route suites under test/server/artifacts and the router's
// guards in app reach the rest through the router.
package itemkit
