# Retaining an expired remote file after an origin miss

Research for [#1645](https://github.com/weaveplatform/artifactweave/issues/1645),
reviewed 2026-11-18. Current documentation comes first; the verified official
7.161.26 sources settle the remaining file-cache transition. No vendor instance
or trial was contacted.

## Current primary sources

- [Remote repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories)
  distinguishes mutable snapshots from immutable releases, direct reads from
  explicit `-cache` reads, and missed-retrieval caching from retained artifacts.
  Its offline/timeout example serves an older cached artifact after expiry.
- [Managing configuration entities](https://docs.jfrog.com/artifactory/docs/managing-configuration-entities)
  describes `hardFail` as failing immediately on a network error.
- [Configuration XSD](https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd)
  defines the retrieval and missed-retrieval periods and the default false
  `hardFail`. These pages do not specify the validation-clock write following
  an actual 404 for an already complete cached file.

These sources do not support treating every failure as an origin miss. Existing
network timeout, disconnect, 429 and 5xx fallback, `hardFail`, and the stricter
HTTP cache rules in RC-3 remain unchanged.

## Official source trace and the clock correction

The [official 7.161.26 source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
has SHA-256
`b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`.
Paths and line numbers below refer to that archive:

1. `backend/core/src/main/java/org/artifactory/repo/RemoteRepoBase.java`,
   `internalGetInfo` (lines 517–539), returns the expired cached resource when
   the remote resource is unfound. `HttpRepo.returnCachedResource`
   (lines 1983–2008) keeps that resource's expired flag.
2. The outer `RemoteRepoBase.getInfo` (lines 450–455) calls
   `unexpireIfExists` for this expired found result. The call is not limited
   to the branch where the remote found unchanged bytes.
3. `repo/service/RepositoryServiceImpl.java`, `unexpireIfExists`
   (lines 2900–2907), reaches `internalUnexpireIfExists`
   (lines 3356–3364), which calls `setUpdated` with the current time.
4. `storage/db/src/main/java/org/artifactory/storage/db/fs/model/DbMutableItem.java`,
   `setUpdated` (lines 336–338), sets `mutableInfo.lastUpdated`.
5. `resource/FileResource.java`, `getCacheAge` (lines 170–176), measures age
   from that field. `repo/db/DbCacheRepo.java`, `isExpired`
   (lines 235–259), compares this age with the retrieval period.

Therefore retaining the copy **advances its validation/expiry clock**. The
ticket's original instruction to leave the entry unchanged followed only the
inner lookup and missed the outer unexpire call. Payload, validators and original
fetch time remain unchanged. The old VFS implementation throttles timestamp
writes at retrieval period zero (`isUpdateRequired`, lines 3370–3388); we use
the ordinary fenced unexpire operation and still retain the copy at zero TTL.

The missed-retrieval entry applies when no file exists in the cache
(`RemoteRepoBase.java`, lines 652–710). A retained complete file does not become
a new negative. An expired historical negative that still owns complete bytes
is cleared when that file is retained. A still-fresh negative keeps its existing
answer; the implementation does not globally bypass missed-retrieval caching.

## Local boundary and evidence

Only explicit origin 404 branches in the shared fetch and Read-only info paths
select this transition. Artifact-kind resources denote files. Metadata must also
carry trusted `RepositoryFile` provenance from an actual repository-file reader:
the item-route resource builder, the existing P2/Alpine/CRAN/opkg/RPM/Debian
physical-file classifiers, Maven's file preparation, or the metadata interceptor's
member-file lookup. These producers cover native non-Maven index-file requests
as well as raw `maven-metadata.xml`. Format-owned API metadata keeps its ordinary
404/negative behavior even when its raw representation is persisted. This
provenance affects only the new origin-miss decision; it changes no admission,
offline, TTL, cache identity or persistence rule. `NoCacheFile` and POST still
exclude a request. A known file barred by the
existing `no-cache`/`must-revalidate` rule is not served and acquires no new
negative entry. That HTTP restriction remains the existing RC-3 inference.

The selected representation must match the current source and the entry token
captured before upstream I/O. Its stored item and blob must still describe the
complete copy. The existing current-authority check and token-fenced transaction
publish only the validation clock and expired-negative removal. Current Read,
patterns, credential/source identity, path epoch and completion revision remain
fences. Read alone can retain an already admitted copy; cold or changed content
still needs Deploy/Cache, as established by #1587.

Real statement failures while rereading or unexpiring this copy retain their
storage cause and answer a sanitized 500 [I]. A live driver deadline joined with
`kernel.ErrStore` remains a store failure; actual ended requests keep their
existing cancellation mapping. Item-store not-found/conflict verdicts keep their
ordinary answers. Blob loss under a stable entry is a storage fault, whereas an
actual concurrent eviction wins through the token fence. This is an internal
integrity/error boundary, not a claimed vendor storage-error API.

Physical verification of an already selected raw metadata blob retains the
content-acquisition phase of its later `Open`, using `ContentOpenError`.
`DbStoringRepoMixin` (lines 492–510) and the default `PropertiesAddon`
(lines 126–128) select a `FileResource` from node/file information; physical
binary acquisition follows inside `MavenMetadataInterceptor`'s tolerant catch
(lines 186–208). See the [metadata fault audit](virtual-metadata-faults-source-audit.md).
An early unmarked `Stat` introduced an abort before that content boundary.
Actual blob deletion reproduced direct 500 and virtual 500 on both SQL backends;
the narrow producer marker restores a healthy later member's virtual 200 while
preserving direct 500, the original cause and unchanged cache state. Entry/item
lookup and clock-publication faults remain unmarked and abort; a current
authority/token failure wins over the content error. The existing audit's
current-version and missing-binary hierarchy qualifications still apply.

CI also exposed an introduced overreach: using only the existing `fileRead`
predicate retained disappeared Bower refs and npm keys. That predicate governs
fill/offline policy and cannot establish which native API documents use ordinary
repository-file lookup. The explicit provenance restores those API contracts
without changing `NoCacheFile` or guessing from a validation variant or path.
There is no source-backed claim here that every unmarked API document is kept
only in memory by Artifactory. Current native API behavior beyond the established
format contracts remains unproven; ordinary `getInfo` is not evidence that every
native API adopts its file fallback. Query parameters and headers cannot opt an
API request into repository-file retention.

The HTTP fixture also incorrectly capped physical timeout exchanges at two.
Go's `net/http.Transport.roundTrip`/`shouldRetryRequest` can replay an idempotent
request across more than one reused connection. The test now observes one
logical policy fetch while retaining real socket-timeout, returned-byte and
unchanged-clock checks, without imposing an unsupported transport-attempt count.

Repeated full-server probes on exact main `d9c304f54` and `9af4ece68` found that
an origin 404 set `NegativeUntil` and advanced the cache revision while preserving
the old blob/item. A 500 left both state and validation time unchanged. Separate
repository incarnations and direct-first/virtual-first orders distinguish the
routes: Maven snapshot virtuals use MVN-02's `DirectRemotes` selection and share
the 404 defect; a Generic virtual's standard walk can fall back to the stored
item. Thus the ticket's older universal virtual/direct comparison is qualified.

Permanent policy and SQLite/PostgreSQL server tests pin GET/HEAD, raw metadata,
Read-only and fill-capable readers, fresh/unique copies, historical/fresh
negatives, strict HTTP rules, network failures, clock preservation/advancement,
and late source, permission, pattern, epoch and replacement fences. The Maven
native scenario uses separate empty artifact caches for its initial resolution,
origin-404 resolution and network-outage resolution.

Unsettled current-version comparisons remain the existing RC-2/RC-3 questions
in [#216](https://github.com/weaveplatform/artifactweave/issues/216), as named in
the [retrieval contract](../implementation/remote-retrieval-contract.md).
This research adds no claim that those comparisons or a new comment were
published or captured.
