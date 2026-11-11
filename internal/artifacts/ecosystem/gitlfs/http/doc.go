// Package gitlfshttp serves Git LFS batch and basic object transfers, and
// the Git LFS locking API, for local, remote, virtual and federated
// repositories on Artifactory's /artifactory/api/lfs/{repo} prefix. The
// object layout is the format package's (artifacts/ecosystem/gitlfs); this
// package answers HTTP with it and keeps path locks in the store.
//
// # Role
//
// artifacts module, ecosystem layer: the native Git LFS batch, transfer and
// locking protocol of the Git LFS slice. It is named gitlfshttp so that it
// does not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type gitlfs.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, stages uploads and
//     stores objects through the item service, where the format's hooks
//     admit them, reads items, blobs and the store's Git LFS locks, builds
//     a virtual repository's member requests, serves items and source
//     results and writes errors; the remote source service, which reads an
//     upstream's batch answers, objects and lock pages for a remote, with
//     the caller's access subject and decider (access/api); the request's
//     base URL (platform/httpkit/baseurl), onto which action URLs point;
//     the caller's principal and the kernel's errors, which lock owners and
//     statuses come from; the generic download rule
//     (artifacts/ecosystem/generic), which every object read meets first,
//     so a blacked-out repository or an excluded path answers 404; and the
//     format package, whose ObjectPath checks every OID.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: a batch's transfer, hash algorithm and per-object upload or
//     download action on this repository, or the object's error (POST
//     objects/batch); an object (GET and HEAD objects/{oid}); an upload's
//     success (PUT objects/{oid}?size=N); a created lock (POST locks), a
//     page of locks (GET locks), a verification's locks split into ours
//     and theirs (POST locks/verify) and an unlocked lock (POST
//     locks/{id}/unlock).
//
// # Invariants
//
//   - A batch is one Git LFS JSON document of at most 1 MiB with no query
//     (415 for another media type), naming upload or download, basic
//     transfer and SHA-256, and 1 to 1,000 objects (400), each a valid OID
//     with a size (422). An object's own error never ends the batch.
//   - An object's size and SHA-256 are checked whenever it is published,
//     served or advertised; an upload has exactly its declared size, and
//     an object already stored with the same identity answers success.
//   - Writes take the write grant on the object's or lock's path. A remote
//     repository is read-only (405). A virtual repository writes to its
//     default deployment repository, a local or federated Git LFS member
//     (405 when none is set, 400 when it is not one), whose grants apply.
//     A caller who cannot read an object gets an upload action whether or
//     not it exists, so a batch never reveals an object it cannot read.
//   - A virtual repository reads its local, federated and remote Git LFS
//     members in order (400 for any other or a repeated member), skipping
//     members the caller cannot read and those that miss, and answers 403
//     when none is readable.
//   - A remote serves a cached object only when it has the requested size
//     and SHA-256, and otherwise asks the upstream's batch API for one
//     object. The answer, of at most 64 KiB, must name that object and size
//     and give one download action whose URL stays on the upstream's origin
//     and names the object, with only safe headers; those headers go only
//     to that exact URL, a cross-origin redirect must use HTTPS, and the
//     object is cached only when its size and SHA-256 match. A 404 or 410
//     object error is a miss.
//   - Every lock route needs the Git LFS media type (415) and an
//     authenticated caller (401). Lock requests are JSON of at most 16 KiB
//     with no unknown field, paths are item paths and refs Git ref names
//     (400 or 422), and a second lock at a path answers 409. A list shows
//     only locks the caller may read, an earlier virtual member's lock
//     hiding a later one's at the same path, and pages through an opaque
//     cursor of at most 4,096 bytes bound to its repository, mode and
//     filter (400), reading at most 16 member pages per request.
//     Verification needs the write grant and splits the locks into ours and
//     theirs; on a virtual repository it fails closed (403) when a member
//     or lock is unreadable. Unlocking needs the write grant, and another
//     owner's lock is unlocked only by force with the delete grant too
//     (403). A remote lists the upstream's locks fresh from its own origin,
//     in pages of at most 256 KiB, answers 502 rather than an empty list
//     when that fails, and refuses lock creation and unlock (405) and
//     verification (403).
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories); decision 0008:
//     docs/research/decisions/0008-virtual-repositories.md (virtual
//     repositories).
//   - docs/implementation/git-lfs-client.md (connecting Git LFS, transfers,
//     lock files, the remote and virtual rules and the evidence),
//     docs/research/36-git-lfs.md and docs/research/35-git-lfs-locking.md
//     (the protocol and locking research), and docs/client-recipes/gitlfs.md
//     (the client recipe).
//   - JFrog Git LFS repositories:
//     https://docs.jfrog.com/artifactory/docs/git-lfs-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/gitlfs) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/787,
//     https://github.com/weaveplatform/artifactweave/issues/1052 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
//
// # Testing
//
//   - protocol_test.go (external, gitlfshttp_test): local, federated and
//     virtual transfers, virtual member grants and write-only disclosure,
//     invalid digests, batch permissions that reveal nothing, and delete,
//     restore and reindex in the batch view.
//   - protocol_fault_test.go (external): malformed batches and unsafe
//     object requests, virtual reads that disclose no missing or denied
//     member, direct reads that honour the digest and declared size, and
//     stale virtual members that fail closed.
//   - remote_test.go (external): remote locks listed fresh and mutations
//     refused, cross-origin lock redirects, oversized and empty cursor
//     pages and unrelated upstream rows refused; the remote cache and
//     offline reads, untrusted actions and tampered bytes, same-origin
//     redirects with the digest pinned, action headers on the exact URL
//     only, client uploads refused, and a cold outage that serves nothing.
//   - locking_test.go (external): create, verify and force; permission
//     filtering and the cursor; revocation; owner unlock; virtual locks in
//     the deployment target, across members, paged in member order and
//     failing closed; and invalid requests that leave state unchanged.
//   - remote_validation_test.go and locking_remote_validation_test.go: the
//     upstream batch, action URL, redirect, cached identity and lock page
//     checks, the lock cursor's binding, and the lock origin guard.
//
// The @gitlfs acceptance scenarios drive git-lfs through local, federated,
// virtual and remote repositories, including two-user locking.
package gitlfshttp
