// Package virtual resolves virtual repositories: the order a virtual
// repository's members are searched in, the repository a deploy to one
// lands in, the merge of metadata that members share, and the cache that
// keeps a merge for virtualRetrievalCachePeriodSecs (decision 0008).
//
// # Role
//
// artifacts module, core layer. Like every core package it never imports the
// module's adapters or an ecosystem http package (decision 0044, dependency
// rules). It imports no store either: core/aql walks members through it, and
// the store's port imports core/aql, so Repos is the one read it makes.
//
// # Inputs
//
//   - A virtual repository's configuration (its ordered members and its
//     defaultDeploymentRepo), read with each member's through Repos: a
//     port.RepoStore, or Configs over configurations a request has already
//     read.
//   - Options: the request's path and whether it names a folder, the Order
//     its caller walks in, and whether upstream steps and
//     priorityResolution apply.
//   - For Answer: a walk's Resolution, the resource path it looked for,
//     whether the caller is anonymous, and the virtual's
//     hideUnauthorizedResources.
//   - For Collect: a walk's members and the format's read of one member's
//     document; for a Merger, the parts Collect returns.
//   - For Cache: CacheOptions (the bounds, which the composition root takes
//     from virtual.metadata_cache, a clock and a logger); for Get, a
//     CacheKey, the virtual repository's period (Period, from its
//     virtualRetrievalCachePeriodSecs) and the format's build; for
//     MemberWritten, the written member's key and Containing, a Repos that
//     lists the virtual repositories naming a key (port.RepoStore).
//
// # Outputs
//
//   - resolve.go: Members, a virtual repository's real members in 7.x's
//     effective order (decision 0008, R1 and R2): Resolve for downloads,
//     Package for package handlers and Stored for the storage API; and
//     Walk, every route from a virtual repository to each member, for the
//     searches and the folder index that judge a member by the route that
//     reached it.
//   - traversal.go: traverse, the single recursive member-graph walk behind
//     Members and Walk. Members supplies global deduplication and nested
//     translation/pattern gates; Walk supplies pre-read chain-cycle checks
//     and route callbacks. Neither policy owns another recursive edge loop.
//   - resolve.go: Resolution and Denial, what a download's walk over the
//     members found and the first member that refused the caller Read
//     (R3), and Answer, the item route's 404, 403 or 401 for a walk that
//     found nothing, under the virtual's hideUnauthorizedResources (R4);
//     a refusal it does not hide is an apierror.Hideable whose Hidden
//     answer the global setting gives (Hidden; #1903).
//   - deploy.go: DeployTarget, the local or federated repository a deploy
//     to a virtual repository lands in, or ErrNoDeployTarget, which the
//     item routes answer with 7.x's 405 (decision 0008, R6).
//   - copies.go: Copy and CopyStore, durable virtual-owned transformed
//     representations, separate from the member items and their listings.
//     Each copy owns a blob reference released on replacement or repository
//     deletion. MVN-03 uses them for cleaned POMs.
//   - merge.go: Merger, the contract a format implements in its
//     ecosystem/<type>/virtual.go to merge its members' metadata, with
//     Part, one member's document; Collect, the members' documents in the
//     walk's order, leaving out each whose read is ErrPassOver or a miss
//     (R3); and the helpers a merge is built from: UnionBy (the first
//     member wins a key), SortVersions (stable), Latest (stable versions
//     first), Accepts (both repositories' patterns, for a merged list
//     filtered before it is paged, R5), and APIBase and ItemBase, the
//     virtual repository's URL a merged document points at. The formats'
//     Mergers are listed with their client/server evidence in
//     docs/implementation/package-format-matrix.md.
//   - cache.go: Cache, the process's merged metadata, each Entry kept by
//     CacheKey (virtual repository, document, member list, the members the
//     caller reads, base URL) and fresh for the virtual repository's period
//     (R8): Get answers a fresh entry or builds it once for every waiting
//     caller, drops the entry when the build finds the document gone, and
//     answers the last entry when a rebuild fails; Evict and Zap drop or
//     expire a document with its members' parts (PartOf); MemberWritten
//     zaps or evicts a document in every virtual repository of the
//     member's type that holds the member, through nesting (RTFACT-25909,
//     Helm's rule).
//
// # Invariants
//
//   - Answer is the walk's answer only: the walk itself, the Read it asks
//     of each member and each remote member's upstream read, is the
//     caller's (itemkit's lookupMember).
//   - Members never returns a virtual repository, and returns each real
//     member once per step: a local or federated member once, a remote
//     member as its cache and its upstream (Resolve), its upstream alone
//     (Package with upstream steps), or its cache alone (Stored, or with
//     NoUpstream).
//   - A cycle, a self-member, a diamond and a member key no repository has
//     never fail a walk; only a member read that fails does.
//   - Walk retains every diamond route and calls a virtual before its
//     descendants. Its borrowed callback chain is clipped against append;
//     callbacks that retain the chain must clone it. Members instead keeps
//     one first position globally, after reading each configured reference.
//   - DeployTarget never returns a remote or virtual repository, whatever
//     the stored configuration names.
//   - Collect keeps the walk's order and never drops a member for a fault:
//     only ErrPassOver and kernel.ErrNotFound leave a member out.
//   - The cache never holds more than its entry and byte bounds, runs one
//     build per key at a time, and never answers a build started before an
//     Evict or Zap of its document as fresh. A period of zero or less is
//     always expired, as 7.x's isCachedResourceExpired is for -1. Member
//     writes invalidate nothing unless a format calls MemberWritten.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 222, and "Remote and virtual repositories", lines 659-665.
//   - Plan: docs/implementation/plan.md#p10-virtual-repositories.
//   - Context map: the artifacts row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md,
//     rules R1 to R11, with their Artifactory 7.161.26 sources.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Delivered P10: effective priority ordering, nesting/cycle/duplicate handling,
// configuration-compatible deployment mapping, miss/denial/hiding answers,
// origin-restricted upstream steps and per-process merged metadata. Item routes,
// storage views, browsing and all adopted package handlers use the shared walk.
// Formats own their metadata mergers, first-holder choices, write routes and
// cache invalidation; the generated type matrix records their mechanisms and
// separate client/server evidence. Item read sessions supply request-local layouts;
// core takes a caller-supplied translator and imports no ecosystem adapter.
//
// Deployment lands in a local/federated DDR with its actual path admission.
// Package protocols retain native refusal envelopes. Cached merges are bounded,
// keyed by current whole-member visibility and request base URL; path-scoped
// readers merge per request. Helm explicitly invalidates containing indexes;
// other member writes do not implicitly invalidate metadata. Source/cache and
// current-authority policy remain the caller's responsibility at every member.
//
// See docs/operations/virtual-repositories.md for R1-R10, family evidence and
// known open defects. #1571 owns final P10 qualification/decision acceptance;
// P13 owns cross-peer federation and P35 multi-node cache coordination.
//
// # Testing
//
// resolve_test.go is an external test package (virtual_test) over Configs:
// a table of every Order over the six groups, priority, nesting, diamonds,
// cycles, self-members, missing keys and nested pattern drops, Walk's
// routes, bounds and failures, independent read/callback traces that pin the
// pre-read versus post-read cycle boundary, and Answer's statuses. deploy_test.go covers
// DeployTarget for each class the defaultDeploymentRepo may name, unset and
// missing, and a read that fails. merge_test.go merges with a test Merger
// built from the helpers alone, as a format's virtual.go would be, and
// covers Collect's pass-overs and faults and each helper's edges. Each
// format's Merger has its own virtual_test.go beside it (gems, luarocks and
// pub so far). cache_test.go drives Cache on a fake clock with counting
// builders: expiry, zero and negative periods, keys, a document gone, Evict
// and Zap with the stale answer, MemberWritten through nesting and cycles,
// single flight under -race, cancellation, a build that panics, late
// builds and both bounds; export_test.go reports its usage.
package virtual
