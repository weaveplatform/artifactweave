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
//   - ServeContent: http.ServeContent for the indexes and metadata the
//     package routes build, answered as the download answers a file.
//   - Kit and its methods: GetItem (an item GET or HEAD), ServeFile,
//     RedirectManifest, Items (the item service), Format, ItemCaller,
//     ItemError, MayBeIdentical, Content, InternalError, Log, Reindex,
//     ReindexAs, OperationRepo, Enqueue, Queue, Await, ServeRemoteStream,
//     RemoteDeliveryAllowed, and ForceAuthentication, the router's
//     middleware that challenges a request without credentials to a
//     repository Forced reports.
//   - Item values and helpers: ItemRequest, ParseItemPath,
//     ParseEscapedItemPath, Found, Download, Lookup, StatusWriter,
//     ResolveRepo, DeployTarget, ItemCreated, ItemNotFound,
//     OverwriteRefusal, ChecksumsJSON, DownloadURI, SplitRepoPath,
//     NotFoundRepo, View and Forced.
//   - Access, Target and AccessFor: the questions handlers ask the
//     request's permission decider.
//   - The per-format remote source requests (AlpineSourceRequest and the
//     others), RemoteResource, and ReindexOps, the reindex operations by
//     package type.
//
// # Invariants
//
//   - ForceAuthentication answers 401 "Unauthorized" with the Basic
//     challenge to every request without credentials under
//     /artifactory/<key>/ (not api, ui or webapp), whatever its method or
//     query, when <key> names a Maven virtual repository with
//     forceMavenAuthentication on or a P2 virtual repository with
//     forceP2Authentication on, before the router routes it and whether
//     anonymous access is on or off, as 7.x's force-authentication filter
//     does (OssForceAuthenticationFilter, 7.161.26). While anonymous access
//     is on, the anonymous user's Basic credentials, a blank Basic user name
//     and a Bearer token that does not verify or whose subject is anonymous
//     count as none, as in that filter; any other credential, verified or
//     not, is left to authentication, and so are the values the filter does
//     not class as Basic or Bearer: "Basic Og==", a lowercase "basic" and an
//     empty Authorization header. Only the repository the path names
//     counts, so a format that reads a member through GetItem never asks the
//     member's flag.
//   - GetItem tries Deps.Overlays in order and stops at the first that
//     answers. A format's route takes precedence over the generic lookup and
//     over every overlay after it, so the composition lists them in one
//     fixed dispatch order: agent marketplace, Alpine, Conda, machine
//     learning, Chef, CRAN, Debian, RPM, opkg, P2.
//   - Deps.Private names the paths whose content a checksum deploy may
//     never reuse. Trash and transfer test the format predicates themselves,
//     because each format relocates and archives its private paths by its
//     own rule.
//   - Item lookups, downloads and deploys keep their routes, statuses,
//     error texts and log lines, which clients and log readers rely on. An
//     archive entry whose archive's blob is lost or cannot be read answers
//     500 "internal error", as a file whose blob is lost does, and logs the
//     failure; once the entry's 200 is sent, a failure is only logged. An
//     entry the archive lacks, or one of a file that is no archive or a
//     corrupt one, answers 404.
//   - A file download, streamed, redirected or from a remote's stream,
//     answers If-None-Match and If-Modified-Since as 7.x's download does
//     (pkgroute.FileNotModified): one If-None-Match line at a time, less a
//     W/ prefix and its quotes, equal to the file's SHA-1, or to its MD5
//     when X-Artifactory-If-None-Match-MD5 asks, so neither "*" nor a list
//     on one line matches; If-Range as http.ServeContent does; a range past
//     its end with 416, Content-Range alone and no body; and it ignores
//     If-Match and If-Unmodified-Since, which Artifactory's download never
//     evaluates. Every index or metadata document a package route
//     builds, through ServeContent, ignores those two as well and keeps
//     http.ServeContent's other answers, and a HEAD of one answers 200 with its
//     whole length whatever Range it sends. A checksum file, local, virtual
//     or remote, answers as 7.x's download answers one: an If-None-Match
//     naming its file's SHA-1, or an If-Modified-Since its file has not
//     changed since, answers 304 with the file's headers, If-Match and
//     If-Unmodified-Since are never read, a HEAD answers 200 with the
//     checksum's length and the file's headers, and a GET sends the
//     checksum alone, dated now, with Range applied whatever If-Range says
//     and a 416 without a body. On a Maven-family remote that stores
//     artifacts locally, a checksum file whose file the cache does not hold
//     is answered from the file's info, the upstream's answer to one HEAD,
//     through the remote's checksum policy, and nothing is downloaded or
//     kept: only pass-thru finds a value, the one the upstream declared, and
//     every other answer is 404 "Checksum not found for <file>". On a
//     Maven-family remote that stores nothing, a checksum file is answered,
//     under every policy, with the upstream's own <file>.<alg>, after one
//     HEAD of the file: its first checksum, or that 404 when the upstream
//     has none.
//     A folder listing and an archive entry keep their own answers.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1314 (the
//     preconditions a download ignores), with
//     https://github.com/weaveplatform/artifactweave/issues/1438 (the
//     documents the package routes build),
//     https://github.com/weaveplatform/artifactweave/issues/1436 (remote
//     checksum files),
//     https://github.com/weaveplatform/artifactweave/issues/1362 (local
//     checksum files), and
//     https://github.com/weaveplatform/artifactweave/issues/1476 (a Maven
//     remote's checksum file for a file the cache does not hold).
//   - https://github.com/weaveplatform/artifactweave/issues/1435 (Force
//     Authentication on the item routes); JFrog's virtual repository
//     settings, https://docs.jfrog.com/artifactory/docs/virtual-repositories,
//     and maven-repositories, "Force Authentication on Virtual Maven
//     Repositories".
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
// the lookup through a virtual repository. access_test.go tests AccessFor,
// and Forced, ForceAuthentication's branches and the credentials it counts
// as none over an in-memory store, and which paths a remote's -cache alias
// reads through the remote source (aliasRemoteMetadata); redirect_test.go the signing-failure
// throttle, the descriptor's minimum redirect size, the request copy
// without the preconditions a download ignores, the request the download
// reads, ServeContent, the checksum file's answers, and that no ecosystem
// handler but Docker's calls http.ServeContent directly; they share one
// file because tools/citestselection lists this package's test files by
// name. kittest.WantDownloadConditionals checks a package route's
// built documents from its own package's tests. The route suites under
// test/server/artifacts and the router's guards in app reach the rest
// through the router.
package itemkit
