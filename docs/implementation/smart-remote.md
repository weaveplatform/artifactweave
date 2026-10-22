# Smart Remote implementation and evidence

Issue [#634](https://github.com/weaveplatform/artifactweave/issues/634) implements the
initial P9 discovery, property, statistics and origin-absence capabilities. It reuses
[source authorization](source-boundary.md), [remote transport](remote-http-transport.md),
[retrieval policy](remote-cache-policy.md) and the persistent work queue. The
[operations guide](../operations/smart-remote.md) specifies timing, ownership, bounds,
recovery and diagnostics. Existing ADR statuses are unchanged. P37 still owns broader
cross-vendor qualification; P19/#216 owns the unresolved current-version observations.

## Evidence and wire choices

| Evidence | Supported conclusion |
|---|---|
| [D] [Current Smart Remote documentation](https://docs.jfrog.com/artifactory/docs/smart-remote-repositories) | Independent statistics/property/source flags; property refresh after metadata TTL; generic absence limitation; Docker digest/tag distinction; Go virtual-source requirement. |
| [D] [Property synchronization guidance](https://jfrog.com/help/r/artifactory-understanding-property-synchronization-and-replication-in-artifactory), 2026-05-24 | Legacy initial property synchronization differs from later Smart refresh; user properties are the synchronization scope. |
| [D] [Self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases), 7.133.8 | Improperly configured Smart remotes should fail rather than silently disable their declared capabilities. |
| [D] [Deprecated API inventory](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis), Remote Repository Configuration | The shared configuration endpoint requires a valid user; non-admin output is sanitized. Deprecation does not remove the compatibility requirement. |
| [S] Historical OSS commit `b99e5649bcad7df04994bd9525e867f5a1af6a39` in [artifactory-oss](https://github.com/knottnhupfer/artifactory-oss/tree/b99e5649bcad7df04994bd9525e867f5a1af6a39), `ResearchServiceImpl`, `SmartRepoVersionFeatures`, `StatsDelegatingServiceImpl`, `ArtifactStatisticsResource`, `StatsImpl`, `ArtifactRestConstants`, `ArtifactoryRequest` | Authenticated version/repository probes, originated header, historical capability minimums, exact statistics endpoint/array fields, and absence of a demonstrated idempotency key. This is historical source, not a current 7.x wire capture. |

The configured repository URL determines one service root and repository key. Discovery
GETs `/api/system/version`, then `/api/repositories/{key}`; a 403 may fall back to
`/api/repositories/{key}/configuration`. Parsed documents are bounded to 1 MiB and must
be JSON objects. Only the sanitized version, repository class/key, service root and
validation time persist, bound to current source and resolved credential revision.
Cached discovery honors metadata TTL; private/no-store discovery is not retained.

Properties use the documented JSON Storage API
`GET /api/storage/{key}/{path}?properties`; existence uses the corresponding item-info
GET. A property 404 alone cannot confirm absence because an existing item may have no
properties. Historical Smart source uses a colon/XML property surface; choosing the
documented JSON endpoint is an implementation choice **[I]**, not a claim that it is
Artifactory's contemporary internal Smart wire. Management requests never redirect;
prepared listing resources remain confined to the selected peer storage subtree on
every redirect hop. Credentials follow shared destination rules.

Statistics PUT to `/api/storage/statistics/{key}` for local sources and
`/api/storage/statistics/{key}-cache` for remote sources. The body is a JSON array with
`repoPath`, `path`, `downloadCount`, `lastDownloaded`, `lastDownloadedBy`,
`remoteDownloadCount`, `remoteLastDownloaded`, `remoteLastDownloadedBy`.
`repoPath` is the upstream item path; `path` identifies the local repository/item for
historical logging. Download timestamps are Unix milliseconds and counts are exact
integers. A bounded complete 200 acknowledgement settles the durable claim. A missing
acknowledgement remains uncertain rather than eligible for resend. Endpoint selection,
zero-valued remote counters, acceptance/status details and current field semantics
remain **[I]** pending contemporary capture.

## Boundaries and storage

`ParseSmartPeer` is a pure endpoint parser; `SmartPeer.StorageURL` cannot select another
authority/repository. Adapters request `source.Resource.RequireSmart` through
`source.Service.Read`, which checks path authorization before discovery or I/O. Prepared
metadata keeps its own validation/variant through Maven admission and does not fetch
Maven checksum companions. Ordinary Maven archives retain their existing validation.
The common and registry controllers retain their separate authorization validators.

`RemoteCacheEntry.Smart` stores bounded ownership snapshots, observation times and
statistics counters; sanitized negotiation lives in `RemoteSourceState.Smart`. Existing
JSON documents provide backwards-compatible persistence without a new SQL migration.
Exact-path paging avoids wildcard scans. The item transaction serializes property
merge, cache publication and ownership propagation across at most 64 representations.
Properties are bounded to 1024 keys/64 values and existing value-length rules; local
ownership tombstones are bounded to 4096 keys. Exceeding a bound fails closed.

Durable jobs contain a credential reference, never a password or bearer token. A
registry reference preserves the original verified grant, realm and expiry while
intersecting current parent-token/user authority; it cannot become a generic HTTP
credential. Worker recovery rechecks the current source and cache generation before
network and commit. Statistics are claimed atomically before submission, so restart
cannot multiply a count. Cache aliases and offline resolution skip the controller.
The admin-only `/api/artifactweave/remote-status/{repoKey}?path=...` extension reports
bounded current state without changing Artifactory Storage responses.

## Qualification and remaining captures

Controlled-peer/native evidence: `smart_remote.feature` (curl/JFrog CLI), Smart tests in
`internal/remote`, registry credential round-trip/revocation tests in `internal/auth`,
source and Maven contract tests, server authentication/status tests, item ownership
regressions, and shared in-memory/SQLite/PostgreSQL Smart-cache contracts. Fake peer
success is not a vendor parity claim.

Add these cases to **RC-8 / #216** for P19 capture:

1. Authenticated discovery and sanitized sharing on current 7.x, including denied,
   disabled, unsupported versions/classes and explicit-empty NuGet context fields.
2. Actual JSON versus colon/XML property requests, initial-only versus TTL refresh,
   system-property exclusions, local edits/deletions, empty-property 404 and confirmed
   absence versus unavailable source; Docker tag/digest and Go virtual-source cases.
3. Statistics endpoint/body/status, local versus remote/cache destination, timestamp
   and remote-count fields, lost acknowledgement/retry behavior and any current
   idempotency contract. Do not change at-most-once handling from an inferred retry.
4. Originated-header grammar and recursive-chain behavior, metadata zap, no-store,
   cache aliases/offline reads and current credentials changed during durable work.

P9 transitive lookup/listing remains with #635/#636 and upstream version search with
#213. These discovery primitives do not complete those separate acceptance exits.
