# Existing-copy cache clock failures

Issue [#1704](https://github.com/weaveplatform/artifactweave/issues/1704)
concerns the existing remote cache clock publication paths: conditional 304
revalidation and making an unchanged copy fresh for a reader without permission
to fill the cache. It is separate from source lookup normalization
([#1692](https://github.com/weaveplatform/artifactweave/issues/1692)) and the
origin-miss retention work in
[#1645](https://github.com/weaveplatform/artifactweave/issues/1645).

## Primary source boundary

The reused official OSS 7.161.26
[source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
has SHA-256
`b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`.
Paths below are relative to that verified, read-only source tree:

- `backend/core/src/main/java/org/artifactory/repo/db/DbCacheRepo.java:208–215`
  resets the cached resource's update time through its mutable filesystem item.
- `backend/core/src/main/java/org/artifactory/repo/service/RepositoryServiceImpl.java:2900–2907,3356–3367`
  delegates to the local unexpiry operation, updates the item and reads its
  information. These methods do not catch the update's exceptions.
- `backend/core/src/main/java/org/artifactory/repo/RemoteRepoBase.java:455,933,1486–1500`
  invokes that local operation when retaining an expired copy or serving an
  unchanged copy. The broad upstream-info exception handler is separately inside
  `getRemoteResource` at 665–687.

This establishes local clock work as a different phase from an upstream
exchange. It does not settle every VFS/transaction exception's exact wire answer
or the behavior of a newer current 7.x release. The separate
[metadata content audit](virtual-metadata-faults-source-audit.md) remains
authoritative for its deliberate content acquisition/read tolerance.

## Reproduction

On main `38e8e4aec0d9c8d9179208f27c662908ad9167fc`, each of these Generic and
Maven cases was run twice against SQLite and PostgreSQL 17.12. Healthy refreshes
returned 200. An injected clock-publication SQL statement failure returned 502
`Bad Gateway`, erasing the original error. Both driver errors carried
`kernel.ErrStore` while their request contexts remained live.

The probe decorates `RemoteCacheStore.Put` to execute the real store statement
`UPDATE root_clock_missing_table SET doc = doc`. It does not rename a production
table or claim to simulate an entire database outage. The permanent regressions
also exercise the current-authority check and a transaction failure after the
actual clock Put, so rollback must preserve the retained entry.

The final branch integrates main
`cbed189bceac60069fd037d9de56235e05877e68`, whose added RPM fix is independent
of the clock error boundary. The original red evidence remains qualified to
`38e8e4aec0d9c8d9179208f27c662908ad9167fc`.

## Cause and bounded correction

`revalidate` and `unexpire` passed their transaction errors through `PolicyError`.
Its default maps unknown failures to protocol 502 and discards their causes;
timeout causes map to 504. That is appropriate for its upstream callers, but
not for a known local SQL statement failure while publishing a validation clock.

Both old callers now use a private clock error mapper. Only a live request's
`kernel.ErrStore` is marked with the existing `item.StorageFault` before the
upstream classifier runs. Its exact cause remains available to `errors.Is` and
the router log. A driver's deadline or `net.Error` timeout cannot turn that
local failure into an upstream timeout or a member miss. All other errors still
use the existing `PolicyError` mapping. No global classifier, origin-miss
handler or source/content error normalization changes.

The 500 `internal error` answer is **[I]**, following the repository's existing
own-storage contracts in
[#1269](https://github.com/weaveplatform/artifactweave/issues/1269),
[#1470](https://github.com/weaveplatform/artifactweave/issues/1470) and
[#1489](https://github.com/weaveplatform/artifactweave/issues/1489). No primary
source cited here or controlled live Artifactory observation establishes an
exact current 7.x answer to these injected local database failures. No specific existing
[#216](https://github.com/weaveplatform/artifactweave/issues/216) comment is
claimed to settle this clock-publication question; its comparison draft remains
local.

## Retained boundaries

- Missing records, token conflicts, superseded sources, Read refusals and ended
  requests retain their existing mappings. An ordinary upstream timeout remains
  an upstream timeout.
- The failed clock transaction leaves the complete cache entry, content,
  validators, validation time and negative state unchanged. A transaction fault
  after Put rolls that Put back. A later healthy read still returns the bytes.
- Receiving a successful upstream response still advances availability state's
  CAS revision before clock publication. The fault does not alter its
  availability fields, increment connection failures or start a cooldown.
- The [#1587](https://github.com/weaveplatform/artifactweave/issues/1587)
  Read versus Deploy/Cache distinction remains: unchanged retained content may
  be refreshed for a reader, while permission to fill a cold cache is separate.
- Metadata content acquisition/read tolerance and `ContentOpenError` retain
  their separate phase. This change classifies trusted clock-publication SQL
  failures, not arbitrary blob, upstream or context errors.

## Evidence

`TestAccRemoteClockStatementFaultsAreInternal` covers both drivers, Generic and
Maven, both refresh paths, check/Put/transaction failures, ordinary and
deadline-joined statement errors, and direct/virtual requests. A virtual has a
usable later local holder; a storage fault must still answer 500. Each failure
has an exact sanitized response, one log containing the original driver cause,
unchanged retained state and a healthy recovery control.

`TestUnitClockPublicationErrorPreservesStorageCauseAndVerdicts` checks exact
cause preservation, driver timeout identity, ordinary refusal/verdict mappings
and ended-request behavior. These controlled tests prove this server's contract;
they do not claim live vendor parity.
