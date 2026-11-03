# 0043: API retry safety and bounded execution

Status: Proposed

Tracked by [#890](https://github.com/weaveplatform/artifactweave/issues/890).
Source review starts at `0d628cbf073933c51af06cc9e594bbd6dafc9d47` (2026-10-28).
This decision describes artifactweave's implementation. It is not a live JFrog
compatibility capture. Header support, rejection statuses and the Access page
cap need confirmation under [#216/P19](https://github.com/weaveplatform/artifactweave/issues/216).

## Standards and compatibility boundary

[RFC 9110 §9.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2)
defines idempotency by intended effects. It does not promise identical response
statuses, nor does it make every operation using PUT or DELETE safely repeatable
when the implementation selects changing resources or causes additional effects.
A timeout or 500 leaves completion ambiguous; a 422 is not a universal rollback
guarantee either. In particular, a key-mismatch 422 can follow a successful earlier
request.

The [Idempotency-Key draft-08](https://www.ietf.org/archive/id/draft-ietf-httpapi-idempotency-key-header-08.html),
published 2026-10-22, specifies a structured string. It remains an Internet-Draft.
The optional append extension below accepts a bounded subset of those strings.
It waits for an in-progress transaction rather than immediately returning the
draft's suggested concurrent-request conflict. No generic retry middleware is
added to generated clients. [RFC 6585 §4](https://www.rfc-editor.org/rfc/rfc6585.html#section-4)
and the [RateLimit draft-11](https://www.ietf.org/archive/id/draft-ietf-httpapi-ratelimit-headers-11.html)
are the references for incoming overload responses; the latter is also a draft.

## Optional protected build append

Only `POST /artifactory/api/build/append/{buildName}/{buildNumber}` supports
`Idempotency-Key`. It is optional. Unkeyed append retains its append-on-every-call
behavior. The header value is quoted and contains 1–128 ASCII letters, digits,
`.`, `_`, `:` or `-`; a quoted UUID is recommended. Multiple header values,
malformed keys, and keys on unsupported routes return 400 before mutation.
Token issuance is explicitly unsupported: no token or other response secret is
stored in a replay record.

The scope is a SHA-256 digest of the authenticated credential reference, operation
and canonical build resource. The reference contains no token or password value;
its password revision prevents an old receipt being inherited by a replacement
credential. Project aliases and timestamps are normalized to the store's
microsecond precision. The key and the exact body bytes are separately digested.
Whitespace changes in JSON therefore count as different intent. Changing the
credential changes the scope: clients must keep the same credential for an
ambiguous request's retries and reconcile before rotating it.

The SQL receipt contains only these digests, the original build ID and its expiry.
It has no serialized HTTP response. Claim, append, build-info item metadata and
receipt commit in one metadata transaction. PostgreSQL uses an insert-on-conflict
claim and row locks; SQLite serializes writers. A different key also locks the
build before reading and replacing its modules, preventing lost updates.
Immutable document blobs written before a failed metadata commit can be orphaned
and remain subject to the existing blob lifecycle; they do not create a second
visible build effect. This protocol must not be used to wrap external side
effects without an outbox or equivalent recovery design.

An active duplicate waits for the original transaction, bounded by the request's
deadline and a 30-second execution budget. If the original rolls back or its
process dies, another request can execute. If it commits, a retry returns 204
without appending. A changed fingerprint returns 422. Budget expiry returns 503
with `Retry-After: 1`; it does not tell the caller whether a concurrent original
request committed. The client must retain the same key and body.

Receipts expire 24 hours after the first successful request's admission time.
At expiry a key can perform a new effect. Each protected request removes at most
100 expired receipts through an expiry index; cleanup rechecks expiry after a
concurrent renewal. There are no durable pending claims or abandoned leases.
Database failures fail the protected request rather than falling back to an
unprotected append. Replicas need synchronized clocks and the same metadata
database; in-memory stores provide no restart durability.

Replays lock and reload the original build by stable ID, refresh the credential
and evaluate current permissions before success. A newer run of the same build
number does not redirect the retry. Deleting the original gives 404; renaming it
gives 409. A recreated resource has a new ID and cannot receive the old append.
Revoked credentials and permissions cannot recover a successful replay response.

## Access identity pagination

Access v2 users and groups keep their existing name cursor and response envelope.
The default and maximum page size are 1000; explicit sizes outside 1–1000 return
400. Continuation is strictly after the last returned name, in either direction.
The cursor is the existing URL-safe base64 name, not a snapshot or query-bound
token. Clients should retain filters, sort and identity while continuing. Changing
them deliberately starts from that name in the new result set; malformed cursors
return 400, and a deleted cursor row still permits continuation.

`ListSummaries` and `ListNames` push filters, authorization-derived resource grants,
ordering and `LIMIT + 1` into the store. User pages select only name, realm and
status, not password hashes or membership collections. Resource filters preserve
the previous exact-target semantics, including inherited group grants. Admin
filtering includes admin groups. Each request evaluates current permissions and
sees committed rows; inserts before the cursor are not revisited, later inserts
may appear, and deleted or newly denied rows disappear. Snapshot isolation is not
promised.

Migration 0018 adds a lowercase search projection populated with Go's Unicode
lowercasing, preserving the old literal substring semantics, including `%` and
`_`. Startup backfills it in resumable batches of 1000. All user/group writes
maintain it transactionally. Do not run older writers against the upgraded schema:
they do not maintain this projection. Plain name continuation uses the existing
name index; arbitrary substring and permission filters can still scan additional
candidate rows. Keyset pagination does not imply universally constant cost.

The representative 5000-user tests log first and late page timings and retain
EXPLAIN output. SQLite reports an indexed `name > ?` search; PostgreSQL reports an
index scan returning 11 candidates for a ten-item late page. See
[PostgreSQL LIMIT/OFFSET documentation](https://www.postgresql.org/docs/current/queries-limit.html)
for deterministic ordering and the cost of skipped offset rows. Legacy unpaginated
and offset APIs retain their existing envelopes and complete result semantics.

## Incoming request budgets and expensive work

The application enables admission controls by default. They apply to the HTTP
boundary, including native protocols and generated stubs. Embedders opt in with
`server.Config.RequestLimits`. `api.enabled=false` disables request budgeting,
not administrative restrictions. Health probes bypass admission. The existing
request-body, upload, archive, AQL and outgoing-source limits remain in force.

Every ordinary request first occupies one of 128 process-local permits and charges
a shared authentication allowance of 120000 attempts per minute. After normal
authentication it atomically charges a shared global allowance of 60000 and its
principal's allowance of 6000. Anonymous and self-authenticating capability routes
share the anonymous allowance at this boundary; no unverified user name or token
claim becomes a counter label. After verifying a capability, the protocol still
checks the named owner's restriction where the capability retains that identity.

Where the boundary can resolve an existing visible repository, it also charges
12000 requests per repository and per owning project per minute. Build operations
can resolve a project directly after checking membership. An unrelated query
parameter cannot redirect a repository's project accounting. Operations spanning
several repositories use the global/principal and applicable cost allowances;
this is not a claim that every child resource has been individually charged.

Management mutations, search, build operations, archive/import/export, VCS, trash,
legacy full repository/identity collections, reindex and deep file lists receive
the expensive classification in `internal/server/request_limits.go`. They have
shared limits of 6000 requests per minute globally and 600 per principal, plus
32 concurrent requests per process and 16 per principal. These defaults allow
normal provider provisioning batches, including Terraform's ten concurrent
operations, while keeping expensive rates ten times tighter than ordinary
rates and reserving half the expensive permits for other principals. All of
these limits remain configurable; deployments can tighten them for their load.
A request context
gets a two-minute deadline. There is no admission waiting queue. Permits remain
occupied until handlers actually return, including after cancellation; the
limiter does not pretend that cancellation has killed a goroutine. Existing
durable background jobs retain their own worker/concurrency limits: an HTTP
permit is not held for the lifetime of a queued job.

Rates use fixed UTC minute windows in the metadata database. All requested
counters are charged or none are. Rejected principal/resource requests cannot
consume another counter's allowance. PostgreSQL serializes the short accounting
transaction with one guard row; SQLite uses its writer transaction. This is a
deliberate coordination bottleneck, not a claim of unlimited request throughput.
Replicas must share the same policy, database and synchronized clocks. An old
window cannot reset a newer stored counter. The in-memory store shares rates only
among services using that store instance. Concurrency limits are per process and
therefore multiply with replicas. Fixed windows allow a burst across a boundary.

Counter keys are hashed and capped at 16384 by default (maximum 65536). Each
transaction prunes at most 128 expired rows; capacity failures preserve cleanup
but charge nothing. Admission storage has a 500 ms deadline. Storage errors,
capacity exhaustion and deadline expiry fail closed with 503 and `Retry-After: 1`.
There is no local fallback that silently gives each replica a fresh shared quota.

Rate rejections return 429 and the ceiling of the remaining blocked window in
`Retry-After`. Responses after principal admission expose only the caller's
selected allowance, for example `RateLimit-Policy: "api";q=6000;w=60` and
`RateLimit: "api";r=5999;t=42`. The policy is `expensive` for expensive requests;
shared-resource exhaustion reports zero remaining without disclosing other
principals' usage. Pre-authentication rate rejection reports `authentication`.
Concurrency rejection returns 429 and `Retry-After: 1`; a rejection before rate
admission has no window metadata, since a permit is not a per-minute quota.
These fields follow draft-11 syntax, not a final RFC or captured JFrog contract.

All defaults are configurable through the `api` application configuration section;
see the [configuration reference](../../operations/configuration-reference.md).

## Temporary restrictions and administrative recovery

The explicit extension `/artifactory/api/artifactweave/api-restrictions/{userName}`
offers administrator-only GET, PUT and DELETE. PUT accepts `until` (a future RFC3339
time no more than seven days away) and a nonempty reason up to 1024 UTF-8 bytes.
DELETE records a clear event. Each change atomically replaces current state and
appends an audit entry with the verified actor, store-assigned sequence and time.
Retries may add audit entries; these routes do not support an idempotency key.
Only managed users can be restricted; there is no new tenant model or automatic
disablement heuristic. Anonymous traffic remains subject to request limits.

Self-authenticating token refresh and OIDC issuance check policy before mutating
token state; a refused refresh preserves the original refresh credential.
Subsequent verified password/token calls and `Current` credential rechecks read
current policy without a cache and return 403 while restricted. The owner checks
also fence existing multipart upload tokens/part signatures, Terraform state
upload capabilities and locally served Terraform signed downloads; signed downloads using `RestoreDownload`, including
Xet, inherit the current-credential check. Already running work is not forcibly
stopped. Previously issued cloud/CDN object-store signatures cannot be revoked by
this application and retain their existing expiry semantics. Restricting one user
does not restrict other users in their project or revoke delegated credentials
whose verified subject is another user.

The exact restriction and audit routes retain credential verification and admin
authorization, but bypass the temporary restriction itself. With request limits
enabled they have a separate fixed allowance of two concurrent requests and sixty
authentication attempts per minute **per process**, with constant-size local
state and a five-second deadline. Ordinary quota, concurrency and budget-table
capacity exhaustion cannot block this reserve. Invalid credentials and non-admins
still fail; a broken identity/restriction database still prevents recovery. There
is no unauthenticated bypass. This is recovery from admission exhaustion, not
availability through database failure.

GET `/artifactory/api/artifactweave/api-restriction-audit` pages the durable audit
newest sequence first, optionally filtering by exact `user`. `limit` defaults to
100 and is restricted to 1–1000. SQL uses `id < cursor ORDER BY id DESC LIMIT ?`,
with a `(user_name,id DESC)` index for filtered pages. New cursors bind version,
filter and administrator name; changing that scope returns 400. They are seek
positions, not authority or snapshot tokens. New changes do not shift continuation.
Every request rechecks administration. Audit history survives user deletion;
current policy is removed, so a recreated name does not inherit the restriction.
Audit retention is currently indefinite and should be included in database
capacity planning; there is no automatic evidence deletion.

## Retained validation

- `storetest` replay cases: concurrent identical requests, scoped keys, mismatch,
  expiry, bounded pruning, failed/nested transactions, cancellation and lock waits.
- `TestAccReplayProcessCrash`: kills a separate process after a mutation inside
  the receipt transaction; both SQLite and PostgreSQL recover without a receipt
  or effect, and the retry succeeds.
- `TestAccReplayPruneDoesNotDeleteRenewal`: waits until PostgreSQL's cleanup is
  blocked on the old row, commits a renewal and proves cleanup preserves it.
- `TestAccAppendReplayDurability`: generated client loses the TCP response after
  commit, retries through another SQL pool, restarts a server, and mixes distinct
  appends with replay requests against both databases.
- HTTP/service tests cover canonical timestamps, current authorization, unchanged
  unkeyed behavior, unsupported secret-producing routes and deadline metadata.
- Identity contract tests cover both directions, Unicode and literal filters,
  direct/inherited grants, permission changes and deleted cursor rows; SQL tests
  cover large pages, query plans and migration/backfill restart.
- Request-budget contracts cover all-or-none charging, capacity cleanup,
  cancellation, concurrent callers and transaction rollback on every backend.
- Admission tests cover shared service instances, principal/resource isolation,
  429 metadata, backend failure and cancellation without leaked permits.
- Restriction tests cover audited concurrent updates, rollback, deletion/recreation,
  credential rechecks, existing signed capabilities and self-recovery while normal
  rate/concurrency capacity is exhausted. Gherkin scenarios exercise the real
  binary, its configuration, rate metadata and restriction/recovery lifecycle.

The [operation ledger and source review](../api-reliability/README.md) cover all
seven specs and native aliases. The [generated-client retry example](../api-reliability/retrying.md)
shows bounded recovery. Focused tests alone do not establish the phase or release
gate; the PR retains the full CI result.
