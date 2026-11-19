# Query-property trace step (#1636)

A stored file selected with matrix parameters now records `Request includes query properties` before its exact-match, missing-key or conflicting-value verdict. The correction changes trace output, with the existing download selection and response retained.

## Primary evidence

The verified [official OSS 7.161.26 source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz) has SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`.

In `backend/core/src/main/java/org/artifactory/repo/db/DbStoringRepoMixin.java`, `getInfo` returns a missing file at lines 499–502, before reading properties. It reads stored properties at line 514 and query properties at line 515, then logs the step at line 518 when the latter set is nonempty. The property comparison follows: `NO_MATCH` at lines 521–523 records a missing-key verdict; `CONFLICT` at lines 524–530 records a conflict and returns an unfound resource. An exact match has no additional comparison verdict. Dynamic Maven metadata takes its earlier return at lines 504–511 and is outside this stored-file correction.

`getImmutableFile` delegates to the immutable item provider (lines 833–835). In `storage/db/src/main/java/org/artifactory/storage/db/fs/model/DBImmutableItemProvider.java`, lines 116–121 reject a folder before returning a file. Therefore this change does not add a property-query prefix to folder traces.

The source establishes the wording and ordering. Existing trace-envelope and matching decisions remain as recorded in the [core API inventory](02-artifactory-core-api-inventory.md).

The same source path applies to cached remote files. `RemoteRepoBase.internalGetInfo` calls `getInfoFromCache` at line 504; `HttpRepo.getInfoFromCache` delegates to the cache at lines 951–952; `DbCacheRepo.getInfo` delegates to its superclass at line 128. `ArtifactoryVirtualDownloadStrategy.processStandard` calls each cache/remote attempt at line 232. Therefore an expired or conflicting copy evaluated again during the remote attempt records another event. This is a separate `getInfo` evaluation, not delivery duplication; path-based deduplication would erase an observed resolution step.

## Implementation and controls

The direct stored-file trace emits the step after successfully reading properties. The virtual member walk emits it at the same point, once per readable stored-file evaluation reached, before conflict or closest-match selection. The virtual root and delivery do not repeat the selected member's step. Missing members, denied readers, folders and requests without selectors add no step. A present selector with an empty value remains distinct from no selector and retains its existing matching answer.

Real SQLite and PostgreSQL 17.12 runtimes reproduce the baseline omission, including a nested walk with one miss and three stored holders. Permanent tests assert the number and ordering of steps, exact and closest winners, conflicts, mandatory keys, empty values, duplicate nested-member suppression, missing paths, denied reads, ordinary GET/HEAD answers and traced HEAD. The pre-existing exact trace arrays remain strict. Property-access failures retain their existing error path and do not become successful matching verdicts.

A follow-up real-runtime audit found the same omission in the direct cached-remote path, which uses `RemoteCopyMatches` outside the two original trace sites. Its exported matcher remains a pure untraced entry point. A private request-scoped wrapper adds the event only after a successful property evaluation of a non-folder item. It adds no property lookup or event for document-only cache entries, missing/cold copies, absent selectors or failed property reads. Direct, cache-alias and virtual tests assert a single fresh successful evaluation, while cache-conflict fallback and mutable-cache expiry each retain two distinct attempts. Ordinary byte/status answers and upstream request counts remain checked.

The original reduced loop was repeated twice per backend before and after the change. The final PR records the measured suite/race/coverage results and distinguishes the changed-package local coverage scope from the repository-wide CI gate.

## Introduced disclosure repaired during qualification

The first cached-remote extension recorded the new event before the existing source boundary's exact-file Read check. A caller admitted by folder visibility but denied the cached file therefore received the new event followed by the existing 403 trace. Repeated decider-backed replay established this introduced regression before publication.

Only the new event now requires the existing `acc.File` decision for the real member and canonical file path. An ordinary request without a trace, or a trace without selectors, gains no extra authorization check. Matching, existing verdicts and the source boundary's own authorization remain unchanged. Permanent tests cover the narrow decider case and actual permission scopes on SQLite/PostgreSQL: an exact exclusion retains its early 403, while a descendant-only include admits folder visibility at the held file path, reaches the remote trace and retains its file-level 403 without the new event. Pre-guard proof is retained separately from the final-source profiles.
