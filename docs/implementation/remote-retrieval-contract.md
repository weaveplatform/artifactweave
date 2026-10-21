# P9 remote retrieval contract

Implementation contract for [#613](https://github.com/weaveplatform/artifactweave/issues/613),
reviewed **2026-10-21**, against main `16d84b59` and the sources below. It elaborates
[ADR0007](../research/decisions/0007-remote-repositories.md), whose status remains
**Proposed**. This is a specification for P9, not a claim that remote fetching works
in the P8 baseline. The existing cache aliases, item service, authorization, format
hooks and durable work queue are building blocks; most remote settings only persist.

## Storage implementation checkpoint (#619)

`Store.RemoteCache` now has matching reference, SQLite and PostgreSQL implementations.
Migration 0015 allocates durable repository incarnations for existing configurations;
repository creation, source edits and deletion maintain those lifetimes transactionally.
`Begin` captures a path epoch and completion revision before upstream I/O. `Put`,
`Negative` and `Revalidate` compare those fences when publishing. `PolicyRevision`
separately fences edits such as offline, TTL and no-store without invalidating the
identity of already cached bytes. `SettingsRevision` partitions entries and cooldown
state when referenced proxy or certificate secrets rotate independently of a repository
edit; the effective-settings resolver and policy must compare it again before publication.

`item.Service.Cache` fully consumes and validates a response through the existing blob
hashing, item and format-hook path. Artifacts use `Cached=true` and the item-owned blob
reference; raw upstream metadata owns a separate reference outside the derived item/index
tree. Item-backed entries verify the current item's hashes before reuse, so replacing a
path with another query/media variant cannot return the wrong variant's bytes. Metadata
is limited to 64 MiB by default; callers may impose a smaller limit. Artifact bodies have
no additional storage-layer size cap and use bounded memory while spooling. No-store
uses an owned temporary reader whose close removes the file, without creating a durable
item, blob reference, raw metadata or package index.

Known checksum headers remain integrity evidence under the selected remote checksum
policy, regardless of `retrieveSha256FromServer`. That flag controls an additional cold
upstream checksum lookup in #621; this storage path always computes all four hashes.
MIME/header validation, HTML error-page rejection, length and checksum failures preserve
the prior entry. For extensionless HTML metadata, adapters must supply a trusted
`ExpectedContentType`; it is never inferred from an upstream header or arbitrary caller
Accept. Format-specific parsing and rewriting of raw metadata remain with the format
adapters. Failed durable candidates are unreferenced blob-GC material. Publication
reuses the existing item service's exceptional content-existence check for a revived GC
tombstone; ordinary upstream transfer never holds a SQL transaction.

`List` exposes bounded keyset pages including raw metadata, negatives and empty path
checkpoints. `Invalidate` preserves those checkpoints to fence late completions and
supports metadata-only expiry separately from full removal. #622 joins explicit path
deletion, zap, restore and cleanup to this invalidation in the same transaction. Its
[maintenance worker](remote-cache-maintenance.md) combines entry usage with successful
item-download accounting, persists bounded scan cursors, and rechecks zero configuration
and repository incarnation before removal. No-store
requests do not call `Begin` merely to manufacture cache state. These APIs are building
blocks: shared HTTP retrieval policy, runtime route activation and native remote-format exit tests remain with #620–#633.

The storage tests cover reference/SQLite/PostgreSQL contracts, upgrade/reopen/down-migration,
source change and delete/recreate during a blocked response stream, SQL statement failure
rollback, raw metadata versus derived indexes, all checksum policies, MIME/length errors,
blob failures, no-store file cleanup and GC races. They prove artifactweave behavior;
existing [I] live-Artifactory capture obligations below remain open.

## Evidence and precedence

The research target remains **Artifactory 7.161.32 self-managed** (7.x review dated
2026-10-17). Current web documentation was checked on 2026-10-21; its later SaaS
behavior is not retrospectively attributed to that target. **[D]** means documented,
**[S]** means primary source, and **[I]** identifies unobserved vendor behavior.
Rules called **implementation decisions** below are what artifactweave must implement;
where public sources do not settle vendor equivalence, the numbered captures retain it
as [I]. An error reported as a vendor bug does not become a compatibility requirement.

| Evidence | Contract established; limits |
|---|---|
| [D] [Remote repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories) | Normal URLs may fetch; cache URLs avoid update checks. Offline uses cache. Retrieval and missing-entry periods differ; ordinary binaries do not expire like metadata. The current page describes ten connection retries but does not bind their timing to 7.161.32. |
| [D] [Zap Cache](https://docs.jfrog.com/artifactory/reference/zapCache) | Zap expires metadata, leaves immutable binaries, and refreshes before returning on the next normal request; unavailable upstreams allow stale fallback. Manage or Delete is required. |
| [S] [provider v12.11.14 remote model](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/remote.go), [client-go v1.55.0 remote model](https://github.com/jfrog/jfrog-client-go/blob/v1.55.0/artifactory/services/remoterepository.go) | Wire field names, checksum policy values, HEAD preflight, and metadata retrieval wait timeout. Provider descriptions are not server traces; its zero retrieval-period allowance does not override the newer minimum validation already implemented in research 03. |
| [D] [Smart Remote](https://docs.jfrog.com/artifactory/docs/smart-remote-repositories) | Statistics, property synchronization and source absence are separate capabilities. Generic absence detection is unsupported; Docker digest pulls do not synchronize tag properties; Go requires a virtual endpoint. |
| [D] [Maintenance](https://docs.jfrog.com/administration/docs/regular-maintenance-operations#cleanup-unused-cached-artifacts) | Unused age makes a cached artifact eligible; scheduled cleanup removes it. Precise activity clocks need capture. |
| [D] [RFC 9111](https://httpwg.org/specs/rfc9111.html), especially sections 3, 4.1 and 4.3 | HTTP cache identity, validators and incomplete representations. Artifactweave's repository cache also has application policy; do not confuse it with a transparent shared HTTP cache. |

Research [03](../research/03-repository-configuration-model.md#remote-fields) remains
canonical for defaults, accepted spellings and validation; [04](../research/04-package-protocols.md)
and each format ADR remain canonical for client protocol shapes. This document owns
shared retrieval ordering, cache identity and the P9 ticket/test allocation.

## Source and representation identity

**Implementation decision — [#616](https://github.com/weaveplatform/artifactweave/issues/616)/#619.** A source has a tagged `SourceIdentity`:
`ExternalHTTP` (configured remote authority and adapter endpoints) or
`ProjectRepository` (source repository identity). It is not just a URL. Keep separate
`RepositoryIncarnation`, `SourceGeneration` and per-path invalidation epoch. Recreating
an identically named repository creates a new incarnation. Never encode credentials in
these identifiers, logs, error messages or durable work keys.

The source boundary accepts an authorized `RetrievalIntent`: `CacheOnly` or `MayFetch`,
plus a separate authorization-derived `UpstreamNetworkAllowed` capability.
It also identifies artifact, metadata, version-query or listing work and the format's
representation kind. A result distinguishes a committed item, a transient body, a
negative result and a typed failure; a successful body has one owner responsible for
closing it. No upstream network request holds a SQL transaction. These semantic requirements
extend the existing `Source` seam; [#616](https://github.com/weaveplatform/artifactweave/issues/616) chooses Go signatures without importing server
packages into format code.

Before cache lookup, flight joining or network access, resolve the actual repository,
check token scope and Read, blackout, include/exclude and path policy. Recheck current
policy before publishing/serving a completed fetch. Normal external remotes may fill
cache on behalf of a Read-only caller; that grants no client Deploy path. Internal
project sources additionally restrict network access: read-only sharing or a read-only
consumer may traverse to producer stored bytes but cannot trigger external upstream fill.
An explicit consumer CacheOnly request never traverses to that producer. Source/share revocation can deny
already-cached content too. [#614](https://github.com/weaveplatform/artifactweave/issues/614)
and [ADR0013](../research/decisions/0013-projects-and-environments.md) own that P11 contract;
never implement it with administrator loopback HTTP or forward the caller's credentials.

A cache/flight identity includes incarnation, source generation, normalized logical
path, representation/media variant and upstream query identity. Sensitive query values
use an opaque keyed fingerprint; do not place them in observable keys. HTTP and JSON
PyPI indexes, NuGet resource versions and OCI manifest Accept variants cannot collide.
Configured credentials, TLS/proxy authority, custom headers/cookies and source endpoints
belong to the generation, not a global client pool. Caller Authorization is never part
of an upstream request. Stateful upstream responses that vary by unmodeled headers or
cookies are transient; do not persist them under a path-only key. `Vary: *` is transient.

**Raw and rewritten metadata.** Retain the validated upstream representation and its
upstream validators separately from the downstream representation. Rewrite URLs only
when rendering a response, through the existing base-URL resolver and adapter policy.
The downstream ETag describes the emitted bytes/variant, including the effective base
URL when it changes those bytes. Never compare a downstream validator to an upstream
one, or forward a caller's conditional header as an upstream validator. Preserve signed
archives, manifests, checksum files and signatures byte for byte. An upstream version
list is authoritative even when none of its listed binaries are cached; reindex may
rebuild the cached-binary index but must not replace that upstream list with a subset.

## Retrieval decision table

Apply authorization first, then cache-only/offline, then no-store, then freshness.
"Usable" below means complete, validated content in the current source generation;
explicit cache-only reads may inspect retained older-generation content if still
authorized. A timeout or failed refresh never advances a success/freshness clock.
Rows R1–R14 are required table-driven cases in [#620](https://github.com/weaveplatform/artifactweave/issues/620), with [#615](https://github.com/weaveplatform/artifactweave/issues/615) recording requests and
fake time. [#621](https://github.com/weaveplatform/artifactweave/issues/621) and the format tickets repeat the applicable rows through HTTP.

| ID | State and request | Upstream work | Response and stored transition |
|---|---|---|---|
| R1 | Unique bytes, absent; normal GET | HEAD then GET unless bypassed | Validate and atomically publish full body; then serve. Failure publishes nothing. |
| R2 | Unique bytes, usable hit | None | Serve indefinitely until explicit deletion or source change; TTL and zap do not refetch it. |
| R3 | Mutable upstream metadata, absent | Bounded fetch | Commit complete raw representation and validators; derive downstream response. |
| R4 | Mutable upstream metadata, fresh hit | None | Serve; client conditional GET/HEAD is evaluated against the downstream representation. |
| R5 | Mutable metadata, expired or explicitly zapped | One shared synchronous conditional refresh | Wait up to `metadataRetrievalTimeoutSecs`; successful 200 replaces, matching 304 revalidates. Error follows R10–R12. No unconditional stale-while-refresh promise. |
| R6 | Locally derived metadata | No independent upstream fetch | Rebuild from its declared authoritative inputs; local `EnsureFresh` is not remote retrieval freshness. A remote view depends on R3–R5, not just locally cached packages. |
| R7 | Fresh negative entry | None | 404, no item/blob/index row. Expiry permits another fetch; zero missing TTL disables negative persistence. |
| R8 | Any `-cache` GET/HEAD; or entrypoint requires CacheOnly | None, including Smart Remote side effects | Serve retained complete bytes, even expired/zapped, or 404. No probe, eager fetch, refresh, remote listing or statistics callback. |
| R9 | Explicit `offline=true` | None | Same cache behavior as R8, regardless of `hardFail`; offline is intentional, not a failed request. |
| R10 | Assumed-offline cooldown | No request until cooldown expires | `hardFail=false`: usable cached bytes or 502 on miss; `hardFail=true`: 502. One permitted demand at expiry probes; peers share it. A fresh/immutable hit that needs no upstream remains a hit. |
| R11 | Timeout, disconnect, 429 or 5xx while refresh is needed | One bounded attempt; no per-waiter retry | `hardFail=false` may return last usable bytes. Otherwise sanitized failure below. Never create 404 or extend freshness. |
| R12 | Upstream 401/403; invalid checksum, MIME or destination policy | No fallback to stale bytes for this normal read | Return sanitized failure; keep prior complete bytes for an authorized explicit cache read. No negative miss, no false successful refresh. |
| R13 | `storeArtifactsLocally=false`, normal online read | Fetch every request; bypass durable positive/negative cache | Stream transient content; do not create item, blob reference, raw metadata, package index or negative record. Existing cache is not erased; R8/R9 can still read it. |
| R14 | Concurrent callers for identical identity and intent | One bounded fill/revalidation per process when storing | Each authorized waiter gets the result; cancellation detaches one waiter. Cancel the fill when the last waiter leaves. No-store bodies are not unbounded multicast buffers; each caller has its own bounded stream. |

Mutable binary names (for example non-unique Maven snapshots) are explicitly classified
by [#625](https://github.com/weaveplatform/artifactweave/issues/625) as refreshable payloads. They follow R3–R5's clock but **metadata-only zap does
not invalidate their bytes**. Delete the payload to force a new download. OCI tag
manifests are mutable metadata; digest manifests and blobs are immutable. A filename
extension alone must not decide mutability.

### HTTP outcomes and body ownership

These are **implementation decisions [I]** for exact vendor status/fallback parity
(capture RC-2). Local permission failures retain existing protocol statuses; the table
concerns failures from an already-authorized source request. Adapters render typed
errors in their established envelope, never relay upstream HTML, cookies, credentials
or authentication challenges as artifactweave credentials.

| Upstream result | Fetch/cache action | Client result when no permitted fallback |
|---|---|---|
| HEAD 200, then GET 200 | GET headers, validators, length and bytes are authoritative if HEAD disagrees; commit only after EOF and validation | 200; normal client range/conditional processing then applies |
| HEAD 405 or 501 | One GET fallback; remember no permanent capability from a single error | GET result |
| HEAD 404 or GET 404 | Record only a confirmed miss for this identity; do not replace good bytes with an error body | 404, including a refresh that confirms deletion; retain old bytes only for explicit cache access |
| HEAD/GET 304 | Requires a complete corresponding stored representation and matching upstream validator; update validation time, no blob write | Serve stored body or downstream 304; unexpected 304 without a usable entry is 502 |
| HEAD/GET 401 or 403 | Never negative-cache or mark assumed-offline; preserve status, sanitize body/challenge | 401 or 403; no stale fallback even when `hardFail=false` |
| 429 | No negative entry or connection-offline state; honor a bounded `Retry-After` cooldown | 429 with sanitized bounded `Retry-After` |
| 5xx | Preserve valid upstream 5xx code, sanitize message; no negative entry | Same 5xx; stale fallback only under R11 |
| DNS/connect failure, broken body, invalid TLS, redirect violation | Transport error; only connectivity/timeouts trip assumed-offline, never security validation failures | 502; no security/integrity fallback |
| Network/metadata deadline | Cancel request; discard incomplete candidate | 504; stale fallback only under R11 |
| Unexpected 2xx, unsafe redirect or unsolicited 206 on full fetch | Not a full cacheable artifact; typed protocol error | 502 |

For downstream HEAD, never publish an item from upstream HEAD alone. The transport sends one HEAD for an explicit HEAD request, and the caller closes
the response without publishing incomplete content. `bypassHeadRequests` suppresses
only the preflight of a GET. [#621](https://github.com/weaveplatform/artifactweave/issues/621) may perform a full, validated GET when its download path needs
computed hashes; that is an explicit fill, not proof that a HEAD cached bytes. A cold
Range request with storage enabled fetches a complete 200 into the ordinary ingestion
path, then serves the requested range; an upstream 206 is never labeled complete. A
no-store range may relay a validated 206 without durable caching ([#621](https://github.com/weaveplatform/artifactweave/issues/621)).

`storeArtifactsLocally=false` is a repository setting; upstream `Cache-Control: no-store`
is a separate response instruction. Treat either as nonpersistent. A response marked
`private` is restricted to its configured repository/source identity; `no-cache` requires
validation, and `must-revalidate` forbids failure-time stale fallback. Do not expose a
cached authenticated response to another repository. HTTP header interaction and the
repository TTL override are [I] RC-3: P9 takes the more restrictive freshness requirement.

No-store artifacts use bounded buffers and do not promise a clean error status after
headers have been sent: a truncated stream must terminate with a read/framing error,
never look like a complete successful download. Policies requiring whole-body checks
may spool to bounded temporary storage before response headers, deleting it on every
exit; this is not a durable cache. Rewritten metadata must be fully parsed/validated
within its size limit before response headers. No automatic retry after any client
body bytes have been sent.

## Atomic state, invalidation and clocks

**[#619](https://github.com/weaveplatform/artifactweave/issues/619)/#622 implementation decisions [I], RC-4.** Fetch to a provisional blob outside
SQL transactions; calculate SHA-256, SHA-512, SHA-1 and MD5 in one pass. At EOF validate
length, upstream checksum policy, MIME and format admission. Publish the item, blob
reference, authoritative raw metadata pointer, cache-state record, format index changes
and required durable work together through the existing item/store contracts. A blob
left by a failed transaction is unreferenced GC material, never a readable item. A failed
refresh leaves the former committed version intact. No-store never acquires a durable
reference. Preserve original upstream checksums separately from calculated hashes:
`pass-thru` may expose an upstream checksum, but it cannot corrupt content addressing.

Cache state needs `fetchedAt`, `validatedAt`, validators, representation kind,
`negativeUntil`, last failure/cooldown and the identity/epoch above. They are distinct
from Item.Created/Modified/Updated and from download/usage statistics. Success times
are stamped on successful completion, not request start or stale access. TTL is computed
from `validatedAt`; 304 advances it only after current-generation validation. On restart,
keep complete data/negatives and deadlines, lose process flights; tolerate backward
wall-clock movement without manufacturing freshness. Use injected clocks in tests.

| Mutation | Required transition and race protection |
|---|---|
| Metadata zap, path-scoped or whole remote | Increment affected metadata/negative epoch; expire mutable metadata, clear matching negatives, retain complete bytes and binary entries. No eager network request. An older in-flight completion cannot undo the zap. |
| Path/cache deletion or unused cleanup | Remove matching positive/negative state, validators and indexes with existing item/blob/trash semantics; advance epoch even if only a negative existed. A matching in-flight fetch cannot resurrect the path. |
| URL, source endpoints/layout, credential, TLS/proxy or request-header/query change | Advance source generation; rebuild client and cookie/token state, invalidate negatives and normal-read reuse. Retain old bytes only for an authorized explicit cache read; never fall back across generations. |
| Offline, hardFail, TTL, no-store or allow/deny policy change | Re-evaluate on each request and at commit; cancel prohibited work. TTL uses existing success time; shortening it can expire an entry immediately. Enabling no-store fences old fills. Cosmetic description/notes changes do not invalidate bytes. |
| Delete/recreate repository | New incarnation; cancel flights/work/clients and remove state. Old completion cannot bind to the reused key. |
| Restore from trash/reindex | Restore bytes as cached content with fresh invalidation epoch, not invented upstream validation time. Reindex reconstructs local indexes; normal remote metadata remains unvalidated until checked. |

Before publishing a candidate, compare incarnation, source generation and path epoch
inside the transaction. If changed, discard; do not retry silently under an obsolete
authorization. A caller retries as a new authorized operation. During a singleflight,
CacheOnly callers never join a MayFetch operation, and a network-forbidden project
caller cannot join an external-fill flight. Multi-node deduplication/leases are
P35; atomic generation checks are required in all stores now.

**Cleanup clock [I], RC-5.** [#622](https://github.com/weaveplatform/artifactweave/issues/622) uses last successful content use, initialized on first
cache commit, as the inactivity clock. Successful GET/range delivery and successful
signed delivery issuance count under existing download-accounting rules; HEAD,
validation probes and failed reads do not. Update activity even when a proxy cache hit
avoids a download counter increment. Recheck eligibility transactionally before delete;
zero disables cleanup. Run bounded durable batches, with protected/derived/sidecar
ownership handled through existing services. The exact vendor activity exclusions and
trash behavior remain a capture obligation, not a reason to leave cleanup inert.

## Transport, security and work bounds

The following are artifactweave limits and decisions, not claimed vendor defaults.
[#617](https://github.com/weaveplatform/artifactweave/issues/617)/#618 implement transport controls; [#620](https://github.com/weaveplatform/artifactweave/issues/620) owns scheduling; [#615](https://github.com/weaveplatform/artifactweave/issues/615) must prove they hold.

- Only HTTP(S) external sources. Reject dot traversal (including decoded variants),
  fragments, userinfo and scheme/authority confusion before requesting. Normalize repeated
  path separators by default; `disableUrlNormalization` preserves them, not traversal.
  Preserve escaped package slashes and signed raw query spelling. Append configured query
  pairs after source pairs and propagated client pairs last, without silently replacing
  duplicate names; [#617](https://github.com/weaveplatform/artifactweave/issues/617) tests order and escaping. Do not append credentials or unrelated
  client query parameters to metadata-provided signed download URLs. `propagateQueryParams`
  is opt-in; meaningful propagated query variants partition response identity.
- At most 10 redirects. Re-evaluate destination policy at every hop, DNS resolution and
  token realm. Explicit private upstream authorities may be configured; metadata may not
  grant itself loopback/link-local/metadata-service or arbitrary internal access. Default
  cross-origin redirects strip repository credentials and sensitive custom headers.
  `allowAnyHostAuth=true` permits repository auth on an otherwise allowed HTTPS redirect,
  not caller auth, proxy auth, a TLS downgrade, or an unrestricted SSRF bypass. Cookies
  stay in a per-source-generation jar with domain/path/secure rules. [#618](https://github.com/weaveplatform/artifactweave/issues/618) records the
  deliberate security restrictions and their compatibility captures (RC-6).
- `socketTimeoutMillis` bounds connection establishment and idle network progress;
  `metadataRetrievalTimeoutSecs` bounds a complete metadata attempt including waiting for
  a shared flight. Zero disables that particular configured timer, not cancellation or
  concurrency limits. A client disconnect cancels its work/wait. Shutdown cancels all
  work and closes idle clients. Metadata/body/error limits apply after decompression.
- Initial parser limits: 64 MiB metadata/listing body, 64 KiB upstream error body,
  1 MiB response headers; stream artifacts with a fixed-size buffer. Oversize metadata
  is 502, not a partially cached success. File size is not constrained by metadata limits.
  [#617](https://github.com/weaveplatform/artifactweave/issues/617)/#619 expose injectable limits for tests; changes need documented compatibility
  evidence, not a silent test-only higher limit.
- Initial admission limits: 8 active upstream operations per repository, 64 per process,
  with at most 256 queued waiters per repository. Excess demand receives 503 rather than
  an unbounded goroutine/queue. Deadlines include queue waits. Reserve no parallel slots
  while waiting recursively for work owned by the same source.
- Transport performs no automatic status retry. The policy permits at most ten consecutive
  connection-error attempts before entering assumed-offline, sharing the attempt counter
  per source generation. Back off between attempts, capped by the assumed-offline period;
  every attempt remains within the caller/metadata deadline. If that deadline expires
  earlier, stop and retain the counter for subsequent demand rather than extending the
  request. Successful communication resets the counter. `assumedOfflinePeriodSecs=0`
  disables assumed-offline; it does not permit an unbounded per-request retry loop. At
  cooldown expiry one on-demand probe is admitted; failure restarts the cooldown, success
  clears it. This scheduling of the documented ten attempts across demand is **[I] RC-7**,
  not a verified 7.161.32 retry timeline. A 429 uses
  a separate per-source cooldown, `Retry-After` capped at 300 seconds (1 second when
  missing/invalid); no automatic retry during the caller's request.
- Eager Maven companions and Smart Remote side effects use durable, deduplicated work,
  at most 3 attempts with backoff per queued operation, then a visible failed state;
  they cannot multiply the foreground request. Cache-only/offline disables their network
  dispatch. [#634](https://github.com/weaveplatform/artifactweave/issues/634) must document a peer statistics idempotency contract before enabling
  retries; otherwise use at-most-once submission and surface uncertainty.
- [#635](https://github.com/weaveplatform/artifactweave/issues/635) listing is nonrecursive by default, at most 100 pages and 10,000 entries per
  query; [#636](https://github.com/weaveplatform/artifactweave/issues/636) uses the documented AQL remote-member limit (5) and rejects cycles. Shared
  source nesting has a visited-identity set and maximum depth 8. Exhaustion is a typed
  error, never a falsely complete empty list. [#213](https://github.com/weaveplatform/artifactweave/issues/213) explicit `remote=1` may use bounded
  listing independently of the browsing UI flag; cache-only searches never do (RC-8).

## Field ownership and required evidence

This matrix covers every common/remote field in research 03 and `pkg/repo.Config`,
including inherited ecosystem fields and the nested `contentSynchronisation` flags. **Stored** means accepted/echoed at the P8 baseline,
not effective remote behavior. **Existing** names a reusable current control. Every P9
row includes a concrete implementation owner and success/denial/failure evidence; all
rows additionally require [#637](https://github.com/weaveplatform/artifactweave/issues/637) create/update/read and secret-masking fixtures. Tests
must assert upstream counts and persisted state, not only response status. [#639](https://github.com/weaveplatform/artifactweave/issues/639) cannot
sign off an unimplemented P9 row. Phase-owned rows are intentionally still inert in P9;
P20 must retain their capability ledger entries until their receiving phase closes them.

| Fields | Baseline; runtime/storage/security owner | Success / denial / failure and invalidation evidence |
|---|---|---|
| `key`, `rclass`, `packageType` | Existing CRUD validation; [#616](https://github.com/weaveplatform/artifactweave/issues/616)/#619 incarnation and source dispatch | Accepted remote vs wrong class/type; no client deploy; delete/recreate race cannot reuse content. |
| `description`, `notes` | Existing metadata/access masking; [#637](https://github.com/weaveplatform/artifactweave/issues/637) | Echo administrator notes, hide public notes; update changes no cache identity. |
| `projectKey`, `environments` | Stored project association; [#614](https://github.com/weaveplatform/artifactweave/issues/614) design, P11/#487 authorization closure | [#616](https://github.com/weaveplatform/artifactweave/issues/616) tagged source seam tests; P11 cross-project read-only/read-write, unshare and deletion tests. P9 does not claim sharing works. |
| `includesPattern`, `excludesPattern`, `blackedOut` | Existing read policy, [#616](https://github.com/weaveplatform/artifactweave/issues/616)/#621 integration | Allowed request fetches; denied/blackout makes zero calls even on cache hit; policy change during fetch fences response. |
| `repoLayoutRef`, `remoteRepoLayoutRef` | Existing layout parsing, upstream mapping stored; [#616](https://github.com/weaveplatform/artifactweave/issues/616)/#625, [#213](https://github.com/weaveplatform/artifactweave/issues/213) | Mapped artifact/version lookup; malformed/unrepresentable path denied before I/O; layout change advances generation. |
| `propertySets` | Existing property-set association; [#619](https://github.com/weaveplatform/artifactweave/issues/619)/#634 | Cached item/property behavior; unauthorized annotation fails; sync failure retains prior complete properties. |
| `priorityResolution` | Stored selection flag; P10 | Ordered/priority merge and permission filtering; P9 must not reorder one-source responses. |
| `downloadRedirect`, `cdnRedirect`, `signedUrlTtl` | Existing delivery machinery/read-only TTL; [#621](https://github.com/weaveplatform/artifactweave/issues/621) | Cold fill then warm signed delivery yields same bytes; unauthorized request gets no signed URL; signing/backend failure preserves cache. |
| `primaryKeyPairRef`, `secondaryKeyPairRef` | Existing signing keys, remote use adapter-specific; [#630](https://github.com/weaveplatform/artifactweave/issues/630)/#632 preserve upstream signatures | Rotation cannot re-sign foreign content or trust stale credentials; invalid provenance/checksum fails. P23/P28 own new signing surfaces, P29/P30 later formats. |
| `xrayIndex`, `xrayDataTtl` | Stored/read-only metadata; P26 | Scanner/index/TTL success and denied/failing scanner proofs belong P26; no P9 scanning claim. |
| `url` | Stored; [#617](https://github.com/weaveplatform/artifactweave/issues/617) transport, [#618](https://github.com/weaveplatform/artifactweave/issues/618) destination policy, [#619](https://github.com/weaveplatform/artifactweave/issues/619) generation | HTTP/HTTPS fetch; reject unsupported/traversing destination; source rotation cannot reuse prior source bytes. |
| `username`, `password`, `enableTokenAuthentication` | Sealed/masked config; [#618](https://github.com/weaveplatform/artifactweave/issues/618) Basic/Bearer, [#623](https://github.com/weaveplatform/artifactweave/issues/623) Docker challenge | Correct auth vs wrong credentials; no caller credential forwarding; rotation invalidates clients/flights/cache identity. |
| `proxy`, `disableProxy`, `localAddress` | Stored plus descriptor proxy config; [#617](https://github.com/weaveplatform/artifactweave/issues/617)/#618 | Recording proxy/bind success, disabled proxy bypass; unavailable proxy/bind fails boundedly; changed proxy rebuilds client. |
| `socketTimeoutMillis`, `metadataRetrievalTimeoutSecs` | Stored; [#617](https://github.com/weaveplatform/artifactweave/issues/617) timers, [#620](https://github.com/weaveplatform/artifactweave/issues/620) fallback | Fake-time delayed headers/body/metadata; cancellation and timeout preserve old cache; no waiter poisons another. |
| `queryParams`, `propagateQueryParams`, `disableUrlNormalization` | Stored; [#617](https://github.com/weaveplatform/artifactweave/issues/617)/#619 | Escaped/slashed/query variants remain distinct; traversal/credential propagation rejected; signed query and source-change tests. |
| `allowAnyHostAuth`, `enableCookieManagement`, `clientTlsCertificate` | Stored; [#618](https://github.com/weaveplatform/artifactweave/issues/618) | Explicit allowed auth/mTLS/cookie flow; host/downgrade/untrusted certificate denial; cookie isolation/rotation/resource cleanup. |
| `customHttpHeaders[].name/value/sensitive` | Stored; sensitive values sealed/masked; [#618](https://github.com/weaveplatform/artifactweave/issues/618) | Header reaches permitted source; CRLF/reserved transport header rejected; sensitive values absent from redirects/logs and other repos. |
| `remoteRepoChecksumPolicyType` | Stored; [#619](https://github.com/weaveplatform/artifactweave/issues/619) integrity, [#625](https://github.com/weaveplatform/artifactweave/issues/625) sidecars | `generate-if-absent`: accept missing, reject mismatch; `fail`: reject missing/mismatch; `ignore-and-generate`: compute; `pass-thru`: retain claimed separately. Test corrupt/partial body and transaction failure. |
| `retrieveSha256FromServer` | Stored generic flag; [#621](https://github.com/weaveplatform/artifactweave/issues/621)/#619 | Enabled cold checksum lookup can fetch upstream sidecar without inventing an artifact; disabled cold lookup 404; mismatch never changes computed blob hash. |
| `blockMismatchingMimeTypes`, `mismatchingMimeTypesOverrideList` | Stored; [#619](https://github.com/weaveplatform/artifactweave/issues/619) with adapter admission | Correct type and explicit exception accepted; login HTML masquerading as archive rejected; absent/unrecognized type follows declared adapter rules, not universal extension guessing. |
| `retrievalCachePeriodSecs`, `missedRetrievalCachePeriodSecs` | Shared runtime freshness policy and maintenance; [#620](https://github.com/weaveplatform/artifactweave/issues/620)/#622 | R2–R7 including exact expiry, zero missing TTL, 304, negative delete/zap; errors cannot extend freshness. |
| `hardFail`, `offline`, `assumedOfflinePeriodSecs` | Stored; [#620](https://github.com/weaveplatform/artifactweave/issues/620) | R9–R12 combinations, fake-clock recovery; 401/403/429 not converted to negative or connection outage. |
| `storeArtifactsLocally` | Stored; [#620](https://github.com/weaveplatform/artifactweave/issues/620)/#619/#621 | R13 full/range/HEAD, source fault/truncation; no new durable artifact/negative/index/blob reference; setting change fences pending fill. |
| `unusedArtifactsCleanupPeriodHours` | Persistent paged maintenance worker; [#622](https://github.com/weaveplatform/artifactweave/issues/622) | Zero/no cleanup; expired vs recently used; concurrent read/fill/delete, restart, sidecar/trash/blob/index consistency. |
| `shareConfiguration` | Existing restricted public configuration view; [#634](https://github.com/weaveplatform/artifactweave/issues/634) discovery | Field whitelist never exposes secrets; unsupported/denied peer discovery fails explicitly. Provider deprecation alone does not exclude this compatibility surface. |
| `synchronizeProperties` | Stored; [#634](https://github.com/weaveplatform/artifactweave/issues/634) initial properties, [#619](https://github.com/weaveplatform/artifactweave/issues/619) atomic storage | Enabled initial properties vs disabled; denied/property-fetch failure cannot partially publish; preserve local annotations. |
| `contentSynchronisation.enabled` | Stored and used by TTL validation; [#634](https://github.com/weaveplatform/artifactweave/issues/634) capability negotiation | Smart peer vs ordinary source; unsupported capability/recursive chain fails explicitly; source change invalidates negotiation. |
| `contentSynchronisation.statistics.enabled` | Stored; [#634](https://github.com/weaveplatform/artifactweave/issues/634) | Cached normal download reports usage when enabled; alias/offline never calls out; duplicate/retry failure cannot multiply counters. |
| `contentSynchronisation.properties.enabled` | Stored; [#634](https://github.com/weaveplatform/artifactweave/issues/634) | Expired normal read refreshes upstream-owned properties, disabled does not; failure keeps prior values; Docker digest pull limitation retained. |
| `contentSynchronisation.source.originAbsenceDetection` | Stored; [#634](https://github.com/weaveplatform/artifactweave/issues/634) | Confirmed origin deletion distinguished from outage/denial; generic limitation explicit; no destructive delete on unconfirmed absence. |
| `listRemoteFolderItems` | Stored/default off; [#635](https://github.com/weaveplatform/artifactweave/issues/635), [#213](https://github.com/weaveplatform/artifactweave/issues/213) | Off causes no browse call; on exposes uncached child without downloading it; pagination/HTML/traversal/auth/timeouts bounded. |
| `handleReleases`, `handleSnapshots`, `suppressPomConsistencyChecks`, `rejectInvalidJars` | Existing local Maven checks, remote intent stored; [#625](https://github.com/weaveplatform/artifactweave/issues/625)/#619 | Allowed artifact vs disallowed release/snapshot or malformed JAR/POM; failed admission preserves cache; no upstream call for known denied kind. |
| `fetchJarsEagerly`, `fetchSourcesEagerly` | Stored; [#625](https://github.com/weaveplatform/artifactweave/issues/625)/#620 durable companions | Enabled fetches at most companion work, disabled none; failure cannot fail an already valid requested artifact or spawn retries without bound. |
| `maxUniqueSnapshots` | Existing local Maven/NuGet work and shared #622 maintenance; [#625](https://github.com/weaveplatform/artifactweave/issues/625)/#629 own remote snapshot-retention integration | Bound cached versions, zero unlimited; cleanup cannot delete current referenced sidecars; interrupted job resumes. |
| `snapshotVersionBehavior`, `checksumPolicyType` | Local deployment semantics already implemented; [#625](https://github.com/weaveplatform/artifactweave/issues/625)/#626 prohibit client deploy to remotes | Do not rename upstream snapshots or apply local upload checksums to fetched content; cache preserves upstream identity; P9 uses remote checksum policy. |
| `dockerApiVersion`, `blockPushingSchema1`, `dockerProjectId` | Existing local Docker rules; remote configuration stored; [#623](https://github.com/weaveplatform/artifactweave/issues/623)/#624 | V2 pull/project scope; no client remote push; challenge denial/manifest digest mismatch. Legacy V1 cannot activate a new remote protocol. |
| `maxUniqueTags`, `dockerTagRetention` | Local tag-retention settings and shared #622 maintenance; [#624](https://github.com/weaveplatform/artifactweave/issues/624) owns remote reachability/retention integration | Remote pulls do not silently apply local push retention; explicit cache deletion/cleanup preserves manifest/blob references. |
| `externalDependenciesEnabled`, `externalDependenciesPatterns` | Stored; [#624](https://github.com/weaveplatform/artifactweave/issues/624) Docker/OCI, [#626](https://github.com/weaveplatform/artifactweave/issues/626) npm, [#630](https://github.com/weaveplatform/artifactweave/issues/630) Helm, [#633](https://github.com/weaveplatform/artifactweave/issues/633) Go seam | Enabled matched URL fetched; disabled/unmatched/unsafe URL denied; redirect/credential leak tests. Bower/CocoaPods use P30. |
| `pyPIRegistryUrl`, `pyPIRepositorySuffix` | Stored; [#627](https://github.com/weaveplatform/artifactweave/issues/627) | Registry/simple/JSON/dist split with variant-correct rewriting; bad external link denied; upstream outage preserves raw metadata. |
| `feedContextPath`, `downloadContextPath`, `v3FeedUrl`, `symbolServerUrl`, `forceNugetAuthentication` | Stored; [#628](https://github.com/weaveplatform/artifactweave/issues/628) v3, [#629](https://github.com/weaveplatform/artifactweave/issues/629) v2/symbols, existing force-auth policy reused | Discovery/restore/symbol download; anonymous force-auth denial; altered feed/signature/failed fetch cannot publish partial metadata. |
| `chartsBaseUrl` | Stored; [#630](https://github.com/weaveplatform/artifactweave/issues/630) | Relative/absolute upstream chart and provenance URLs; external allow-list rejection; retain upstream index entries for uncached charts after reindex. |
| `terraformRegistryUrl`, `terraformProvidersUrl` | Stored; [#631](https://github.com/weaveplatform/artifactweave/issues/631)/#632 | Module/provider discovery and mirror bytes/signatures; unsafe URL or wrong repo type denied; source change/expired URL recovery. `terraformType` remains disallowed on remote; `terraformbackend` remains a disallowed remote class. |
| `vcsType`, `vcsGitProvider`, `vcsGitDownloadUrl` | Stored; [#633](https://github.com/weaveplatform/artifactweave/issues/633) handles GOPROXY source seam only; P28 Go VCS, P33 shared VCS | P9 Go source table/failure proof; P10 native virtual proof; later VCS clone/ref/auth/depth/archive tests. No shell fallback in P9. |
| `curated`, `passThrough` | Stored/inert; P27 Curation/PTC | Explicit policy allow/block/audit and user-context privacy tests in P27; P9 never claims scanned/approved content or forwards caller context by default. |
| `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl`, `cargoAnonymousAccess`, `cargoInternalIndex`, `podsSpecsRepoUrl`, `hexPublicKey` | Stored/inert; P30 | Native Bower/Cargo/Composer/CocoaPods/Hex metadata and artifact/signature tests, unauthorized/malformed source faults; reuse P9 transport and identity. |
| `forceP2Authentication`, `forceConanAuthentication`, `enableConanSupport` | Stored/inert; P31 | P2/Conan native client and forced-auth denials; unsupported metadata fails explicitly. |
| `debianTrivialLayout`, `ddebSupported`, `calculateYumMetadata`, `yumRootDepth`, `yumGroupFileNames`, `optionalIndexCompressionFormats` | Stored later-format settings; P29 | Linux metadata/signature/index and malformed upstream tests; remote authoritative metadata is never synthesized from cached subset. |
| `archiveBrowsingEnabled`, `enableFileListsIndexing` | Stored switches with partial existing archive APIs; P28 | Native archive inspection/indexing toggle/permission/bomb tests; [#621](https://github.com/weaveplatform/artifactweave/issues/621) fetching bytes does not complete these flags. |

Fields exclusively for virtual aggregation (`repositories`, `defaultDeploymentRepo`,
`artifactoryRequestsCanRetrieveRemoteArtifacts`, `virtualRetrievalCachePeriodSecs`,
`externalDependenciesRemoteRepo`, `pomRepositoryReferencesCleanupPolicy`, `keyPair`,
`forceMavenAuthentication`, `hideUnauthorizedResources`, `resolveDockerTagsByTimestamp`,
`useNamespaces`, `debianDefaultArchitectures`, `p2Urls`) belong P10 and the relevant
format phase; `members` belongs P13, `encryptStates` to the implemented local Terraform
backend, and `forceMetadataNameVersion`/`forceNonDuplicateChart` to local Helm admission.
Their presence in a shared JSON model is not a new P9 remote capability. Future keys
absent from this baseline must enter P20's ledger with an implementation owner.

## Adapter and phase handoffs

| Owner | P9 proof / receiving boundary |
|---|---|
| [#615](https://github.com/weaveplatform/artifactweave/issues/615), [#637](https://github.com/weaveplatform/artifactweave/issues/637), [#638](https://github.com/weaveplatform/artifactweave/issues/638), [#639](https://github.com/weaveplatform/artifactweave/issues/639) | Deterministic status/body/time/auth faults; provider apply/empty-plan/destroy separately from content; optional real-upstream monitoring; final native/client, security and CI sign-off. Fakes are not vendor observations. |
| [#623](https://github.com/weaveplatform/artifactweave/issues/623)/#624 Docker/OCI/Helm OCI | Challenge tokens, immutable digests, mutable tags, foreign layers, exact manifest bytes; source auth failure cannot become `MANIFEST_UNKNOWN`. |
| [#625](https://github.com/weaveplatform/artifactweave/issues/625) Maven/Gradle/Ivy/SBT; [#626](https://github.com/weaveplatform/artifactweave/issues/626) npm | Preserve full upstream version metadata; validate snapshots/POM/JARs and packument/tarball integrity. Eager companions are bounded; npm audit and local signing [#362](https://github.com/weaveplatform/artifactweave/issues/362) remain P28. |
| [#627](https://github.com/weaveplatform/artifactweave/issues/627) PyPI; [#628](https://github.com/weaveplatform/artifactweave/issues/628)/#629 NuGet | Correct HTML/JSON and v2/v3 variants, upstream signature/core-metadata integrity, no invented local-only view of remote versions; names, pages and rewritten downloads stay consistent. |
| [#630](https://github.com/weaveplatform/artifactweave/issues/630) Helm; [#631](https://github.com/weaveplatform/artifactweave/issues/631)/#632 Terraform | Preserve index/discovery lists, chart provenance, module ZIP/provider checksum/signature bytes; validate external origins. Terraform backend is a different local-only repository contract. |
| [#633](https://github.com/weaveplatform/artifactweave/issues/633) Go; P10 | P9 tests GOPROXY `.info/.mod/.zip`, list/latest source without enabling unsupported direct remote routes. P10 composes ordered sources and proves native Go; P28 sumdb/VCS and P33 VCS transport remain assigned. |
| [#634](https://github.com/weaveplatform/artifactweave/issues/634)/#635/#636 and [#213](https://github.com/weaveplatform/artifactweave/issues/213) | Smart capabilities, explicit browsing, transitive AQL and upstream version search must be observable before P9 closes. They reuse identity/auth/cache bounds rather than creating unbounded alternate clients. |
| P10 | Source result includes freshness, raw metadata, representation identity and fetch intent. Permission-filtered merging must not turn an upstream failure into absence or enable remote I/O through current recursive item lookup before P10 lands. |
| P11 / [#487](https://github.com/weaveplatform/artifactweave/issues/487) | Project sharing/authorization and the two absent admin endpoints; single replication and general package cleanup. Replication cannot transfer freshness/credential state as portable content or confuse a rewritten view with authoritative input. |
| P12 / P27 | Full descriptor/global settings (including configurable transitive member limits); Curation/PTC enforcement/user context. Stored flags remain visible as inert until these phases deliver them. |
| P29–P33 / P35 / P37 | Additional package formats consume this contract; multi-process deduplication is P35; cross-vendor Smart/replication interoperability is P37. None is claimed by P9 fake-peer success. |

## P19 capture cases for issue 216

These [I] cases are implementation-ready decisions above, with vendor equivalence still
unverified. Track them on [#216](https://github.com/weaveplatform/artifactweave/issues/216)
under the IDs here. Each capture records target build, settings, request timing/counts,
status/headers/body hashes and cache/storage observations. P9 proceeds without a live trial.

| ID | Capture |
|---|---|
| RC-1 | Expired vs zapped metadata, immutable and non-unique binary, normal vs `-cache` GET/HEAD; change upstream bytes and delay response to distinguish synchronous refresh from background serving. Repeat zap with a fresh negative. |
| RC-2 | HEAD success/disagreement/405/501/404; conditional 304 without body; refresh 404/401/403/429/5xx, timeout and disconnect with warm/cold cache and hardFail on/off; record envelopes and stale choices. |
| RC-3 | Toggle no-store with an existing cache; normal/offline/alias HEAD/GET/Range and upstream Cache-Control/Vary; inspect negative persistence, checksum reads, truncation and downstream validators after rewrite/base-URL change. |
| RC-4 | Rotate source URL, credentials, proxy/certificate and metadata endpoints during a blocked fill; race zap/delete/recreate/restore/reindex; distinguish retained bytes from reusable normal-read state. |
| RC-5 | Cleanup eligibility after GET/range/HEAD/signed redirect/probe/failed read and restart; observe exclusions, trash and sidecar/index behavior. |
| RC-6 | Credential and sensitive-header/cookie behavior across same/cross-origin redirects with lenient auth on/off, HTTPS downgrade, metadata external URL and token realm; never use production credentials. |
| RC-7 | Actual 7.161.32 connection retry counts/delays, assumed-offline recovery/zero behavior, concurrent callers, metadata timeout0, 429 Retry-After; compare with current documentation's ten-retry statement. |
| RC-8 | Upstream version search with browsing disabled, cache-only listing, listing bounds and Smart chaining; confirm absent vs unavailable, statistics retry and property ownership/deletion. [#634](https://github.com/weaveplatform/artifactweave/issues/634) adds exact peer wire captures. |

Public documentation does not define artifactweave's internal storage schema, queue
sizes or generation mechanism. Those are deliberate engineering choices, tested locally;
only externally visible differences require parity captures or a documented deviation.

## Shared policy implementation checkpoint

The [shared cache policy](remote-cache-policy.md) implements R1–R14, bounded
concurrency and durable cooldown state. [Generic activation](../operations/remote-repositories.md)
is implemented by #621, including native curl/JFrog CLI evidence. [Cache maintenance](remote-cache-maintenance.md)
is implemented by #622: metadata-only zap, deletion/restore fencing, persistent unused
cleanup and native curl rediscovery after negative-entry deletion. Native protocols
and remaining discovery/synchronization exits retain their P9 owners.


### Native adapter admission boundary

Native adapters retain a canonical repository path for authorization, cache records,
usage and publication. A trusted adapter may select a separate escaped upstream path
or metadata-derived absolute URL; the latter still passes the shared DNS, redirect and
credential-origin checks. Target, metadata checksum pins and expected content type
participate in the opaque cache identity. Empty hints preserve existing generic keys.

Complete-byte validators and bounded auxiliary retrieval run outside SQL, before either
cache publication or transient delivery. Metadata checksum pins are mandatory even when
the repository ignores optional upstream checksums. Validators identify their schema in
the representation variant. Current caller, source and credential checks still run at
publication and delivery; a completed auxiliary request supplies no new authority.

This boundary enables native adapters; it does not activate a protocol by itself.

### Metadata authorization and final delivery

A native adapter may authorize a metadata representation against its containing
folder with `Resource.PermissionFolder` and `PermissionPath`. This is permitted
only for metadata/version resources; artifact reads always require their exact
canonical path. Folder authorization is an admission check, not permission to
disclose every upstream version. The adapter filters metadata with the caller's
access and repository patterns, records each disclosed artifact path, and invokes
`Result.CheckRead` for those paths in its final `Revalidate` callback. `CheckRead`
cannot widen the original folder, performs no fetch, and repeats current caller,
source and credential checks. Revoking one version while another remains readable
must prevent the stale response from being delivered.

Adapters may also authorize an unmodified metadata file by its exact path (for
example Maven XML); this grants that file's complete contents and does not grant
access to its referenced artifacts. Rewritten responses retain the result's
revalidation and usage callbacks but replace upstream byte-dependent validators
and headers. `pkgroute.Kit.ServeResult` performs the final fence and records usage
only after a successful GET 200/206 delivery; denied, failed, HEAD and 304 responses
do not extend artifact retention. Source metadata remains separate from indexes
computed from downloaded cache items.
