// Package source is the read boundary shared by remote package adapters. It
// is below HTTP routing and outside format write hooks: neither a resolver
// nor a body reader may run inside the transaction that publishes a cached
// item.
//
// # Role
//
// artifacts module, core layer: the authorised read boundary shared by
// adapters. Service.Read checks a request's path, permission and repository
// restrictions before any backend work, resolves the durable source
// identity, hands the read to a Backend under a Permit, and fences what
// comes back against a changed source or withdrawn authority.
//
// # Inputs
//
//   - Driving: the routes and format slices that read a remote repository
//     (artifacts/adapters/http, pkgroute and the ecosystem slices), and core
//     remote's discovery and listings, through Service.Read with the Request
//     and Resource they build.
//   - Driven: the Backends the application installs for the HTTP and
//     cache-only reads (core remote's Policy, or a format backend over it
//     such as Maven's or Docker's), Resolve for the source identity,
//     Revalidate for the caller's current authority, and access/api's
//     Decider, which answers each Request's permission questions.
//   - The generic download policy (generic.CheckDownload), which every
//     authorised read also passes: at its permission path, or at the
//     Resource's PolicyPath, the path Artifactory 7.x caches the document
//     or file at, when the adapter names one, and only the repository's
//     blackout when the Resource is BlackoutOnly (Resource.CheckPolicy).
//
// # Outputs
//
//   - Service and its Read; Backend; Request, Resource, Permit,
//     RetrievalIntent and ResourceKind; Result, Stream, Representation and
//     MetadataResponse.
//   - The closed adapter contracts: CocoaPodsGitSource and
//     ValidGitLFSActionHeaders.
//   - The safe failures (ErrDenied, ErrInvalidRequest, ErrUnsupported,
//     ErrUnavailable, ErrSuperseded, ErrInvalidResult) and HTTPError with
//     its FailureKind, which routers map to their own error envelopes.
//   - Resource.CheckPolicy, the policy rule Service.Read applies, which the
//     application's Revalidate fences a result with too.
//   - A Result's descendant checks: CheckRead and CheckFolder, which apply
//     the download policy as Read does, and CheckPermission, which checks
//     Read permission alone, for a listing that keeps a release whose file
//     the repository's patterns reject, as Artifactory's listings do.
//
// # Invariants
//
//   - Path, permission and repository restrictions run before any source
//     lookup or backend call; a Request without a decider is refused.
//   - A Permit only narrows: no hop borrows a more privileged flight's
//     result, and a cache-only read never reaches the network.
//   - A backend failure leaves as a safe failure, kernel.ErrNotFound, the
//     context's error or an HTTPError with a bounded status and
//     Retry-After; upstream bodies, URLs, headers and credentials are never
//     error messages. The download policy's refusal leaves as its own
//     *format.RejectionError, from Read and from CheckRead and CheckFolder
//     alike, since artifactweave writes its message: a listing that checks
//     each disclosed release can keep one whose file is refused, and a read
//     answers the policy's 404 (#1237). A failed read of the server's own
//     copy under check (format.ErrStorageFault) leaves as it is too, in
//     the store's words, so a router answers it as a server fault rather
//     than as ErrUnavailable (#1269).
//   - The package opens no store transaction and propagates none.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md,
//     lines 26 to 29 (the source boundary), and
//     https://github.com/weaveplatform/artifactweave/issues/614.
//   - docs/implementation/source-boundary.md and
//     https://github.com/weaveplatform/artifactweave/issues/616.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (core/remote/source keeps calling
//     generic.CheckDownload).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 221 (core/remote/source) and line 1040 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1028
//
// # Status
//
// Implemented P9. Moved here in P9d.
//
// # Testing
//
//   - source_test.go, source_boundary_test.go, root_test.go, smart_test.go,
//     policy_test.go and storage_fault_test.go are an external test
//     package (source_test) over fake backends: read intents and
//     representations, refusals before lookup, result validation and safe
//     errors, a storage fault passed on as it is, stream ownership and late
//     fences, root metadata grants, the Smart and Git LFS contracts, and the
//     policy path, blackout-only reads, policy refusals from CheckRead and
//     CheckPermission's permission-only check.
//   - authorize_test.go and cocoapods_test.go are in-package unit tests of
//     the path authorisation and the CocoaPods Git descriptor.
package source
