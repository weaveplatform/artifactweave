# Shared remote retrieval policy

Issue [#620](https://github.com/weaveplatform/artifactweave/issues/620) implements
`internal/remote.Policy` as the HTTP/cache backend for the
[source boundary](source-boundary.md). It follows the
[remote retrieval contract](remote-retrieval-contract.md), using the
[bounded transport](remote-http-transport.md) and durable cache tokens. Generic
route activation is implemented by [#621](../operations/remote-repositories.md);
native format activation remains with its P9 adapter tickets. This backend alone
does not enable every remote protocol.

## Retrieval and freshness

Unique artifact bytes remain usable until deletion or source change, subject to
more restrictive upstream HTTP cache directives. Mutable payloads and raw metadata
expire under `retrievalCachePeriodSecs`; mutable payloads remain distinct from
metadata for zap. A format's derived indexes never initiate independent upstream
reads. Raw upstream metadata has its own blob reference and cannot be replaced by
a local package reindex.

An expired representation sends its own upstream ETag or Last-Modified validator.
Downstream client validators and ranges are not upstream cache validators. A cold
range requires a complete validated body before downstream range selection; a
206 response cannot become a complete cached artifact. A matching 304 advances
validation time and applies supplied Cache-Control/Vary/validator fields. A
conflicting validator is a protocol error. A HEAD uses HEAD only, can validate
existing bytes, and never publishes an empty body as a full artifact.

Only 404 creates a negative entry. It expires under
`missedRetrievalCachePeriodSecs`; zero disables both its use and persistence.
A confirmed 404 retains prior bytes for explicit cache reads but prevents normal
stale fallback, including after negative expiry until upstream succeeds again.

Explicit offline and cache-only reads perform no transport or credential
resolution. They return retained bytes, including raw metadata, or 404 regardless
of hardFail. Cache-only raw metadata selects the matching opaque representation
variant from durable records; it does not synthesize an upstream list from local
packages.

## Failures and recovery

Connection failures and timeouts accumulate across demands. Ten consecutive
failed attempts open the configured assumed-offline interval; no background retry
loop is created. An HTTP response proves connectivity, but a 200 body must finish
validation before it clears the failure count. Setting
`assumedOfflinePeriodSecs=0` immediately disables an existing interval. Resolved
source credential changes reset its availability state.

During cooldown, a request needing upstream returns eligible stale bytes when
hardFail is false, otherwise a sanitized failure. A fresh immutable hit remains a
hit. One source-wide demand probes when the interval expires; concurrent paths
wait for that outcome before resolving their own representations. Availability
state survives a process restart. A transient/no-store recovery probe validates
and discards its owned body before each caller obtains its own response.

429 records a separate Retry-After cooldown, bounded to 1–300 seconds, and never
becomes a negative or a connection outage. Timeouts, disconnects, 429 and 5xx may
fall back to complete prior bytes unless hardFail or HTTP cache rules prohibit
it. 401/403, invalid destinations, corrupt checksums, MIME failures and incomplete
validation cannot fall back. Failed refreshes do not advance validation time.
The original failure travels with a shared stale result so every waiter applies
its own hardFail policy.

## Concurrency and publication

Singleflight keys include repository incarnation, source generation, effective
credential revision and an HMAC of representation identity: escaped path, resource
kind, mutability, adapter variant, Accept and opt-in propagated query. Raw signed
queries and credential material are never cache identifiers or log fields.

A canceled waiter detaches without canceling its peers. The last departing waiter
cancels the shared operation. Admission defaults to eight active callers per
repository, 64 globally, and 256 queued callers per repository; saturation returns
a sanitized 503. Metadata deadlines include waiting for capacity and retrieval.
The HTTP transport can retry a failed reused connection as allowed for idempotent
GET/HEAD; the policy adds no per-waiter retry loop.

Network/body work precedes the short publication transaction. Durable tokens
check incarnation, source generation, path invalidation epoch, content revision
and policy revision. Current authorization and resolved credentials are checked
again before publication and after shared work. Zap/deletion/configuration changes
cannot be undone by a late completion. Local successful-use accounting updates a
separate monotonic activity clock without invalidating a pending refresh token.

The existing item tree has one binary slot per repository/path. Distinct binary
query or Accept variants cannot serve one another's bytes: replacing the slot
makes the prior variant miss and refetch. Raw metadata variants retain independent
blobs. Multi-node fetch coordination remains owned by P35; durable publication
fences already apply across store users.

## No-store and delivery ownership

`storeArtifactsLocally=false` bypasses positive and negative cache state on every
normal request. It leaves retained cache bytes available to explicit offline or
cache-only reads. Complete response validation uses an owned temporary file with
bounded memory; closing the result removes that file. It publishes no item, blob
reference, package index, raw metadata or negative entry.

An upstream no-store response or unsupported Vary dimension cannot be shared as a
stream. A shared candidate is discarded and each waiter fetches its own transient
response. The initial bounded attempt can leave an empty fencing record, never a
cached representation. This extra request is intentional to retain independent
body ownership and complete validation.

Metadata defaults to a 64 MiB limit. Artifact transfers stream with fixed memory;
the caller owns the complete result body. Persisted results carry a `Used` hook
for successful GET/range or signed-delivery issuance. Lookup, upstream probes,
HEAD, 304 and failed downstream streams do not call it. #621 implements delivery
accounting; #622 implements [cache maintenance](remote-cache-maintenance.md), joining
zap, path deletion, restore and persistent unused-cache cleanup to durable fences.

## Evidence

`policy_test.go`, `policy_fault_test.go`, `flight_test.go` and `rules_test.go` cover
the R1–R14 contract, including exact fixture counts, fake-time expiry, shared
cancellation, source/config/delete/zap races, variants, HTTP cache controls,
HEAD/304, integrity failures, outage recovery and no-store body ownership. The
storage contracts separately exercise restart and transactional behavior on
in-memory, SQLite and PostgreSQL stores. These fixtures prove ArtifactWeave's
implementation; live Artifactory observations remain tracked by
[#216](https://github.com/weaveplatform/artifactweave/issues/216) for P19.
