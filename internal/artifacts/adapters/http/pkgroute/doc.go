// Package pkgroute is the router kit for package formats with a protocol of
// their own: npm and Go in P7, then PyPI, NuGet, Helm and Terraform
// (decision 0032). Mount claims /artifactory/api/<name>/{repoKey}/... for
// one protocol, or /artifactory/api/<name>/... when the protocol finds the
// repository key elsewhere in the path (Protocol.Resolve), and any routes
// it has at the host root (Protocol.Host). Before the protocol's handlers
// run it resolves the repository, checks its package type and passes it
// through the class gate, so every protocol answers those the same way and
// in its own envelope. The kit's helpers then check permissions, apply
// Artifactory's download policy to the files they find, download through
// the item routes' download path, deploy through the item service and read
// multipart uploads (ReadForm).
//
// Docker's registry predates the kit and keeps its own router (decision
// 0031).
//
// # Role
//
// artifacts module, HTTP adapter layer: the router kit for native package
// protocols. The router in app and the ecosystem protocol packages
// (ecosystem/<type>/http) mount their protocols through it. It never imports
// app, whose router hands it what it needs through Options, and it reaches the
// access module only through access/api: the caller is the kernel principal,
// and permissions are checked with the request's accessapi.Decider on
// accessapi.Repo values.
//
// # Inputs
//
//   - Mount: a chi router, Options (the store, the item service, the item
//     routes' download, deploy, error mapping and created answer, and the
//     fault recorder) and the Protocol: its name, package types, error
//     profile, reserved operations, absent paths, key resolution, anonymous
//     challenge, host routes, signed requests and handlers.
//   - The store, a Store: the artifacts module's stores and transactions
//     (artifacts/core/port), with the settings and configuration documents,
//     the work queue and the lease table (platform/settings,
//     platform/configdoc and platform/work), which handlers hand on through
//     DB() to the item service, a format's Env and the package index. It
//     holds no access store: permissions come from the request's decider.
//   - The request's principal and decider in its context, which the
//     server's authentication middleware put there.
//   - Remote: a protocol's opt-in to the common remote source boundary,
//     over the source service, which Kit.Read reads through.
//
// # Outputs
//
//   - The mounted routes, and the Kit each handler receives.
//   - Store, the store the kit resolves repositories and reads items in, and
//     DB() returns. Each composite store in app satisfies it, so the server
//     passes the store it holds.
//   - Permission helpers: Can, CanFolder, Require and RequireFolder.
//   - Download helpers: DownloadItem, ResolveDownloadItem, ServeItem,
//     ServeFound, Read, CheckDownload, ServeResult, ServeStream and
//     SourceError; and 7.x's download's conditional answers, which the item
//     kit shares: UnevaluatedPreconditions, WithoutUnevaluatedPreconditions,
//     WithoutHeaders, FileNotModified, ETagValue, WriteNotModified and
//     RangeRefusalWriter.
//   - Metadata helpers: StoredItem, ResolveStoredItem, ServeMetadata,
//     ServeFoundMetadata, ReadMetadata, CheckDocument, CheckBlackedOut,
//     MergeMembers and IsRefusal; and ReadServed and ServedReadFault, a
//     protocol's read of the copy the source service served it.
//   - Deploy helpers: Store, StoreOnce, StoreNew, StoreResolved, Upload,
//     Deploy, DeployItem, Created, Annotate, Delete and DeleteTree.
//   - Multipart helpers: ReadForm, ReadFormLimited and ReadFormWith; path
//     and header helpers: ParsePath, SplitValues, DeclaredChecksums and
//     SetChecksums.
//
// # Invariants
//
//   - A protocol's handler runs only for an existing repository of one of
//     its package types that the class gate admits; anything else answers
//     in the protocol's own envelope before it.
//   - A deploy goes through the item service, so formats' hooks and the
//     item routes' rules apply as they do to any other write.
//   - A file that DownloadItem, ResolveDownloadItem, ServeItem, ServeFound
//     or Read finds in a local or federated repository, or in one as a
//     virtual repository's member, is refused, after the caller's read
//     permission, when the repository is blacked out or its include and
//     exclude patterns reject the path, whether the file exists or not and
//     for administrators too (generic.CheckDownload); CheckDownload gives
//     the same answer to a protocol that reads a stored blob itself. The
//     refusal answers 404 with Artifactory's message, and is
//     kernel.ErrNotFound to a protocol too. IsRefusal tells it from a
//     missing item, so a protocol with its own "not found" passes the
//     policy's answer on unchanged. A remote repository's own policy is
//     left to the source boundary, which applies it to every read.
//   - A virtual repository's own include and exclude patterns are checked
//     once, at the path the client asked for: CheckDownload or
//     CheckDocument on the virtual repository's request, before its members
//     are searched, or MergeMembers for a merge, refuses a path they reject
//     with "No repository found to serve the request". A member request
//     (MemberRequest) checks them too before a member's file is looked up
//     or read, at the read's PolicyPath when it names one, so a search
//     without that first check still refuses, with a refusal IsRefusal
//     reports; a listing never applies them. A member's own refusal, a
//     remote member's from the source boundary included, is the virtual
//     repository's "Could not find resource", which says nothing of how the
//     member is set, and is no refusal to IsRefusal, so the search passes
//     over that member to the next.
//   - The metadata helpers apply no download policy to what they find:
//     a listing built from StoredItem, ResolveStoredItem or ReadMetadata
//     keeps a release whose file the policy refuses, as Artifactory's
//     listings do, and a lookup behind a write, such as whether a deploy
//     replaces a file, is the write's to refuse. A protocol refuses a
//     metadata document with CheckDocument at the path Artifactory 7.x
//     stores or caches it at, a remote repository's own policy included,
//     and resolution metadata whose 7.x storage is not documented with
//     CheckBlackedOut, before it computes anything; a native search keeps
//     its answer. CheckDocument also refuses a file that a download finds
//     through metadata reads, at the file's path and before those reads. A
//     protocol builds a virtual repository's merged document from the
//     members MergeMembers returns, which leave out a member that is
//     blacked out or would refuse the document's path.
//   - Remote.Read holds every format route's remote read without a format
//     hint to 7.x's block list of upstream labels as the item route is held
//     (source.ClientMIME, with no Accept, so the type of the URL asked for
//     is the one requested): a refused label answers 409 with 7.x's message
//     in the protocol's envelope, remembered for the missed-retrieval
//     period, never the upstream's 502. A read with a format hint keeps the
//     adapter's stricter check and its 502
//     (https://github.com/weaveplatform/artifactweave/issues/1392).
//   - Resource never carries the client's Accept: no header of a client's
//     request reaches the upstream or the cache identity, as 7.x's
//     HttpRepo.whiteListHeaders sends none, and an adapter whose upstream
//     negotiates sets its own
//     (https://github.com/weaveplatform/artifactweave/issues/1390).
//   - ServeStream answers a stream that names its repository path, the
//     remote file an artifact read returns or the item route serves, with
//     the type of that path, as the file's kept copy answers, and never the
//     upstream's label, as 7.161.26's RemoteRepoResource.getMimeType does; a
//     stream without one, a document a protocol builds or raw metadata it
//     serves under its own type, keeps its MediaType
//     (https://github.com/weaveplatform/artifactweave/issues/1477).
//   - ServeStream answers If-Match and If-Unmodified-Since never, as 7.x's
//     download reads neither, on every stream. A stream with a SHA-1, a
//     file, answers If-None-Match and If-Modified-Since as that download's
//     isNotModified does (FileNotModified), and a range past its end with
//     416 and Content-Range alone; a document a protocol builds per request
//     keeps http.ServeContent's answers to both
//     (https://github.com/weaveplatform/artifactweave/issues/1468,
//     https://github.com/weaveplatform/artifactweave/issues/1469). A file's
//     ETag is its bare SHA-1, never the upstream's, unless the route chose
//     its own (https://github.com/weaveplatform/artifactweave/issues/1484).
//   - SourceError answers a failure of the server's own storage while it
//     keeps a remote file (format.ErrStorageFault) as the router's
//     internal fault, 500
//     "internal error", whatever the store's error wraps, never as the
//     upstream's 502 or 504
//     (https://github.com/weaveplatform/artifactweave/issues/1269). A
//     protocol that reads the copy the source service served it, the
//     server's cached file or spool file, marks a failed read the same way
//     (ReadServed, ServedReadFault), and keeps its own 502 for a copy that
//     reads but does not parse or is over its limit
//     (https://github.com/weaveplatform/artifactweave/issues/1462).
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the protocol kit, the class gate and the generic download policy).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote content through the source boundary).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission checks).
//   - Research 24: docs/research/24-package-format-handlers.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 238 to 246 (artifacts/adapters/http) and line 1021 (the package
//     mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals and
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1074 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P7: the kit with npm and Go (pull request 330). P8 added
// host-root routes, encoded repository keys and multipart uploads (pull
// request 580) for PyPI, NuGet, Helm and Terraform; P9 the remote source
// boundary (pull requests 644 and 654); P9b and P9c the protocols that
// followed. Moved here in P9d, which narrowed the kit's store to Store
// (https://github.com/weaveplatform/artifactweave/issues/1089).
//
// # Testing
//
// The package's own tests mount a test protocol over the in-memory store,
// authorised by the access test kit: pkgroute_test.go, resolve_test.go,
// folder_test.go, form_test.go, lifecycle_test.go, storeonce_test.go,
// source_test.go, source_storage_test.go, source_delivery_test.go,
// path_test.go,
// download_test.go, which covers the download policy at each helper, and
// document_test.go, which covers CheckDocument and CheckBlackedOut, a
// read's policy path and a remote member's refusal against a fake source
// boundary; source_result_test.go tests ServeResult from inside the
// package; conditional_test.go covers ServeStream's conditional answers and
// FileNotModified, and etag_test.go a file stream's SHA-1 ETag. Every
// native protocol's route suite under test/server/artifacts reaches it
// through the router.
package pkgroute
