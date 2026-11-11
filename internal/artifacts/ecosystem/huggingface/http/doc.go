// Package huggingfacehttp serves the native Hugging Face Hub client protocol
// for model and dataset repositories using Artifactory's current ML layout,
// under /artifactory/api/huggingfaceml/{repoKey}: the Hub API (repository
// info and trees by revision, repository and branch creation, preupload and
// commit), resolve downloads, Git LFS batch and signed uploads, the opt-in
// Xet tokens and CAS routes, and an administrator's bounded migration of a
// legacy revision into the current layout. Identities, paths and the Xet
// wire formats are the format and xet packages' (artifacts/ecosystem/huggingface
// and its xet); this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Hugging Face Hub, Xet and
// legacy-migration protocol of the Hugging Face slice. It is named
// huggingfacehttp so that it does not shadow net/http. Outside tests, only
// its own http subtree, the module's http adapters and app may import it
// (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type huggingfaceml, local,
//     remote, virtual or federated; the Xet CAS routes and a signed LFS
//     upload carry their own capability, so the router serves them without
//     authenticating them.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads and writes items
//     and writes errors; the access contracts in Options (access/api),
//     which recheck the credential a Xet token or an LFS upload grant names
//     and evaluate its permissions, and scan the permissions a legacy
//     migration would change; the keyring (Options.Keys), from whose master
//     key the capabilities are signed; the remote source service
//     (Options.RemoteSources), which fetches upstream Hub metadata and
//     files into the remote cache; and the format and xet packages.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository class
//     and the Artifactory error profile.
//   - Answers: Hub repository info and trees, with a mutable ref resolved to
//     its full revision; snapshots published by a commit in one
//     transaction; file downloads, from Git LFS, from Xet chunks or, for a
//     remote, from the verified cache; short-lived Xet read and write
//     tokens and CAS answers (xorb and shard uploads, reconstructions and
//     signed chunk ranges); and the legacy migration's plan or result.
//
// # Invariants
//
//   - A revision is immutable once published, and a virtual repository
//     reads a whole revision from one member, so files of different models
//     never mix.
//   - Xet is off unless Options.XetEnabled: its token routes then answer 501
//     and resolve answers advertise no Xet metadata, while Git LFS stays
//     available.
//   - Every capability is signed, bounded in size and short-lived, and each
//     use rechecks the caller's credential, permissions, virtual membership
//     and selected revision; knowing a blob's digest is no authority to
//     publish or read it.
//   - Xet records are published only after every reconstruction has passed
//     its Xet root, segment verification and SHA-256 checks, and a remote's
//     Xet answers derive only from its verified cached file.
//   - A legacy migration keeps its source items, publishes one verified
//     revision atomically, and refuses a move that would change any
//     permission's access.
//   - Artifactory's download policy, after the caller's read permission:
//     a file of a blacked-out repository, or one the include and exclude
//     patterns reject at its snapshot path, answers resolve 404 with the
//     item routes' message before any header of the served file is set,
//     and its Xet reconstruction and chunk ranges answer the same unless
//     another file of the revision with the same content may be served. A
//     blacked-out local or federated repository answers info and tree with
//     the blackout's 404, naming the model's folder, and a virtual
//     repository passes over a blacked-out member, local or remote, when it
//     chooses the member that serves a revision. The patterns leave a file
//     listed, and the Xet read token is still issued: the reads it
//     authorises refuse.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and the protocol and storage decision); decision
//     0007: docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/huggingface.md (connecting the Hub client, Xet and the
//     evidence) and docs/operations/huggingface-migration.md (the legacy
//     migration, an artifactweave extension).
//   - JFrog Hugging Face repositories:
//     https://docs.jfrog.com/artifactory/docs/hugging-face-repositories,
//     and Hugging Face's download guide, which names the storage hosts a
//     remote may be redirected to:
//     https://huggingface.co/docs/hub/models-downloading.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/huggingface) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/779,
//     https://github.com/weaveplatform/artifactweave/issues/1036 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Moved here in P9d.
//
// # Testing
//
//   - protocol_test.go, read_test.go, create_test.go, validation_test.go
//     and boundary_test.go: native commits, reads and refs, signed LFS
//     uploads, repository and branch creation, and malformed or unsafe
//     requests; TestAccPinnedClient drives the pinned Hub client (the CI
//     acceptance job sets HF_CLIENT_PYTHON).
//   - remote_test.go and virtual_test.go: remote caching, redirects,
//     integrity and offline reads; virtual member selection.
//   - xet_boundary_test.go, xet_cache_test.go, xet_fence_test.go and
//     xet_remote_test.go: Xet capabilities, ranges, the metadata cache and
//     the revalidation of every delivery.
//   - migration_test.go, migration_scope_test.go, migration_policy_test.go,
//     migration_fault_test.go and migration_boundary_test.go: the legacy
//     migration, its token scopes, its permission scan (with the access
//     test kit, access/accesstest), its faults and cancellation.
//   - lifecycle_test.go: delete, restore and reindex through the item
//     service.
//
// The route suite test/server/artifacts/huggingface checks the download
// policy on resolve, info, tree and Xet, directly and through a virtual
// repository (TestAccHuggingFaceDownloadPolicy and
// TestAccHuggingFaceVirtualDownloadPolicy).
package huggingfacehttp
