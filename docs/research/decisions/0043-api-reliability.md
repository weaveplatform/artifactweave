# 0043: API retry safety and bounded execution

Status: Proposed

Tracked by [#890](https://github.com/weaveplatform/artifactweave/issues/890).
Source review starts at `0d628cbf073933c51af06cc9e594bbd6dafc9d47` (2026-10-28).
Amended by [#1401](https://github.com/weaveplatform/artifactweave/issues/1401)
(2026-11-12) with the rules every operation follows, under the API principles
baseline [#1395](https://github.com/weaveplatform/artifactweave/issues/1395):
see [Rules for every operation](#rules-for-every-operation).
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
The optional key extension below accepts a bounded subset of those strings.
It waits for an in-progress transaction rather than immediately returning the
draft's suggested concurrent-request conflict. No generic retry middleware is
added to generated clients. [RFC 6585 §4](https://www.rfc-editor.org/rfc/rfc6585.html#section-4)
and the [RateLimit draft-11](https://www.ietf.org/archive/id/draft-ietf-httpapi-ratelimit-headers-11.html)
are the references for incoming overload responses; the latter is also a draft.
On 2026-11-12 the IETF datatracker lists -08 and -11 as the current revisions of
both working-group drafts, so neither has moved since this record cited them. The
header syntax stays at -11 until this record is amended for a later revision.

## Rules for every operation

Every operation in the seven specs under `api/src` declares three classes:
`x-idempotency`, `x-cost` and, when it returns a collection, `x-pagination`. A class
describes how the operation answers now; a 501 stub declares the class it is meant
to have once served, except where that would change how it is admitted today (see
[Cost classes](#cost-classes-and-how-admission-finds-them)). The rules below hold
the server, the generated code and the [operation ledger](../api-reliability/README.md)
to those classes. Where served behavior does not yet meet a rule (only build
append accepts a key; several lists are still collected before they are sliced),
the ledger records the gap.

JFrog's contract comes first. On an operation JFrog defines, every documented
parameter, body and status stays. The rules add only optional extensions that
JFrog's clients never send or can ignore: the `Idempotency-Key` request header,
and the `RateLimit`, `RateLimit-Policy`, `Retry-After` and `Link` (`rel="next"`)
response headers, each marked `x-artifactweave-extension: true` in the specs. A
request that uses none of them answers as before, apart from added response headers.

### What 7.x documents

The sources are in the research section
[7.x evidence for the operation rules](../api-reliability/README.md#7x-evidence-for-the-operation-rules).
In short:

- No 7.x documentation page, release note, KB article or public Jira issue
  documents `Idempotency-Key` or any other request deduplication header. 7.x is
  retry safe only where a request names its target: a create with an existing key
  is refused, and a delete or replace names what it removes or replaces.
- 7.x documents 429 for concurrent search and AQL requests (from 7.84, per user
  from 7.146.7 self-managed, switched off by default since 7.84.16) and a rate
  limit on NuGet search (7.125.4), with no response header. 503 appears only on the
  JFConnect readiness and liveness probes and the cluster lock diagnostics. No page
  documents `Retry-After`, `RateLimit`, `RateLimit-Policy` or `X-RateLimit-*` on a
  JFrog answer.
- 7.x's lists page in five ways: `cursor` and `limit` (Access v2 users, groups,
  permissions and projects; Grid's `page_token`), `offset` and `limit` (Get
  Repositories since 7.173, the retention history, Distribution's `start_pos`,
  SCIM), page numbers (cleanup runs, federation failures), the package protocols'
  own (Docker `n` and `last`) and AQL's `.offset()` and `.limit()`. Most legacy REST
  lists have none.

So each rule reaches a JFrog operation only as an extension, and its syntax
follows the IETF drafts above rather than a JFrog convention.

### Idempotency classes

`x-idempotency` takes one of four values. The author picks the first that holds,
in this order:

1. **`safe`**: no intended effect beyond reading
   ([RFC 9110 §9.2.1](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.1)).
   A POST that only reads (a search body, a status query, a signed part URL) is
   safe; a GET that acts, such as the CRAN archive move, is not. Effects the client
   did not ask for, such as filling a remote cache, download statistics, logs and
   rate counters, do not count.
2. **`natural`**: the request names its target, and repeating the identical request
   converges on the state one request gives, though the answer may differ (201 then
   409, 204 then 404; RFC 9110 §9.2.2). Examples: a create that refuses a
   duplicate, create-or-replace, delete, deploy to a path, setting or clearing a
   property or flag, a rename or move (a repeat finds the source gone), and
   recomputing an index. `x-idempotency-natural` lists the request elements that
   name the target, each as `<in>:<name>`: `path:repoKey`, `query:to`,
   `header:<name>`, `body:/username` (a JSON Pointer,
   [RFC 6901](https://www.rfc-editor.org/rfc/rfc6901.html), into a JSON body),
   `body:archive` (the coordinates an uploaded package declares), or the single
   entry `route` when the route itself names a singleton (the system configuration,
   the password policy, every user). An operation is not natural when the server
   picks the target as it runs (the latest run, a generated ID or name) or when each
   call adds something (appended modules, a promotion record, a new credential).
   Records of each request, such as audit entries, events and logs, do not count,
   since RFC 9110 lets a server log every request: the restriction PUT below adds
   an audit entry per call and is natural. Natural does not guard against another
   client's change between two attempts; a repeated delete removes whatever the
   name then means. A client that needs that guard reads the target to reconcile.
3. **`key`**: an action that is neither, whose whole effect commits in the metadata
   transaction (see [Replay](#replay-for-keyed-operations)) and whose answer carries
   no secret. It accepts the optional `Idempotency-Key` once it declares the shared
   `IdempotencyKey` parameter, which it does only when the server honors the key
   for it. A stub may be declared `key` as its intended class; it declares no
   parameter.
4. **`none`**: no retry protection exists, and `x-idempotency-reason` says why with
   one value from this closed list:
   - `secret-response`: the success answer carries a credential or private key
     (access and refresh tokens, legacy API keys and tokens, pairing tokens, the
     OIDC token exchange). A replay would have to store the secret or issue a second
     one, and both are refused. The client reconciles by listing its credentials and
     revoking any it did not receive.
   - `protocol-reconciliation`: a step of a multi-request protocol with recovery
     state of its own (multipart upload sessions, OCI upload offsets, Terraform
     state locks, Git LFS locks). The protocol's identifiers and status operations
     reconcile it, and its clients send no key.
   - `external-effect`: the request waits on an effect outside the metadata
     transaction that cannot roll back with it: a call to another service, peer or
     upstream (replication, distribution to edges, a webhook test, federation sync)
     or a write to the server's file system (export). Work enqueued inside the
     transaction is not external (see Replay).
   - `read-or-write`: one operation whose body chooses between a read and a write,
     so no single class fits. AQL is the case: `find` queries read, while its
     delete and update actions write. Clients retry proven reads and reconcile
     writes.

   A new reason is added only for a case none of these covers, with its
   justification recorded here.

**A key where none is accepted.** A request whose `Idempotency-Key` reaches an
operation that does not declare `IdempotencyKey` answers 400 before
authentication, admission and routing, as it does today. That covers `safe`,
`natural` and `none` operations, `key` operations that do not declare the parameter
yet, hand-written aliases and native protocol routes, unknown paths, and 501 stubs:
a stub sent a key answers 400, not 501. Two or more `Idempotency-Key` fields answer
400 too. The 400 uses the path's error envelope, is not charged to any allowance
and carries no rate header. Its message named build append while append was the
only keyed operation; since delete and promotion accept a key too (#1410) it names
the rule instead, which changed only answers to requests that use the extension.
Reasons:

- It is today's answer, so no request changes.
- The draft says a client cannot assume that a server honors a key it sends
  (§2.5.1). Refusing is the only way to tell the client its retry is not protected;
  silently ignoring a key on a `none` operation would let a client repeat a
  credential issue or a replication in the belief that it was safe.
- On `safe` and `natural` operations a key adds nothing, and one rule (a key is
  accepted exactly where the spec declares the parameter) is one a guard can check.

The gate finds the operation in the generated operation table before routing (see
[Cost classes](#cost-classes-and-how-admission-finds-them)), so it needs no route.

### Replay for keyed operations

Every keyed operation uses one replay, built on build append's design below: the
receipt names the committed resource, and a per-operation reload renders the
answer. No answer is stored.

- **Scope and fingerprint.** The scope digests the authenticated credential
  reference, the operation id and the canonical resource the operation derives from
  its path and query: aliases resolved and timestamps at the store's microsecond
  precision, as append's project, name, number and started are. The fingerprint
  digests the exact body bytes. A query parameter that changes what the effect does
  without selecting its target, such as a dry-run or overwrite flag, is digested
  into the fingerprint with the body, so changing it under the same key answers
  422. A parameter that only checks consistency, such as append's `buildRepo`, is
  in neither. A key needs a credential with a stable reference; anonymous and
  registry-token callers get the operation's 401 or 403, as append's do.
- **One transaction.** The replay prunes at most 100 expired receipts, then opens
  one metadata transaction with the request's context (`tx.Runner.Tx`,
  `internal/platform/tx`). Every store call the handler and its services make with
  that context joins it, nested `Tx` calls included, so the claim, the whole effect
  and the receipt commit together or not at all. The effect returns the positive,
  stable ID of the resource it created or changed, and the receipt keeps it.
- **Receipt.** As append's: the three digests, the resource ID and the expiry, 24
  hours after the first successful request's admission. No status, header or body
  is stored.
- **Replay answer.** A retry with the same key and fingerprint finds the committed
  receipt and does not run the effect. The operation's reload locks and loads the
  resource by its stable ID, rechecks the current credential and the current
  permissions, and answers the operation's success status with headers and body
  built from the committed state. A deleted resource answers 404, one renamed or
  moved out of the scope 409, and a revoked credential or permission its 401 or
  403. When nothing changed in between, the answer equals the original. Where the
  success body reports what the effect did rather than describing a resource
  (promotion messages, copy and move results, batch reports), the effect stores that
  report with its own committed state, bounded and free of secrets, and the reload
  returns it unchanged.
- **Failures and waits.** Only a committed success has a receipt. An error rolls
  the receipt back with the effect, so a retry runs the operation again and can
  succeed or fail anew. A changed fingerprint answers 422. A duplicate that arrives
  while the original runs waits for it within the request deadline and the 30-second
  budget, then answers 503 with `Retry-After: 1`. Every retry is admitted and
  charged like any request and carries its own rate headers.
- **Where it runs.** A table that `make generate` emits lists the operations
  declared `key` that declare `IdempotencyKey`. The replay wraps a generated
  handler as strict-server middleware, which receives the operation id, and a
  hand-written override as route middleware.
- **Blobs written first.** Content-addressed blobs may be written before the
  transaction opens, as append writes its document. A rollback or crash can orphan
  them; the blob lifecycle collects them, a retry writes the same digest again, and
  no visible effect is doubled.
- **Queued work.** Work is enqueued inside the transaction
  ([decision 0033](0033-persistent-work-queue.md), "Enqueue within the change"),
  and in-process follow-ups run through `AfterCommit`, so a job exists exactly when
  its receipt does and a replay enqueues nothing. What the job then does, calls to
  peers included, the worker retries under the job's own work key.
- **External calls.** No call to another service runs inside the replay's
  transaction. An operation that must make one while the client waits is `none`
  with `external-effect`, unless the call moves behind the outbox
  ([decision 0021](0021-peer-trust-and-sync-protocol.md)) or the work queue and is
  enqueued inside the transaction.

A generic store of each answer's status, body and selected headers was rejected
because it would break three guarantees this record already makes:

- The receipt holds no serialized response, so no secret or personal data enters
  the replay store, even when an operation's answer later gains a field.
- A replay re-evaluates the current credential and permissions, so revoked access
  cannot recover an answer.
- Receipts stay small and of fixed size for 24 hours of keyed traffic.

The draft asks only for "a response consistent with the request being completed
according to the user's intent" (§2.6), not a copy of the first one. The cost is a
reload for each keyed operation; most actions are natural, so few need one.

### Cost classes and how admission finds them

`x-cost` takes one of four values:

- **`ordinary`**: the shared authentication, global and principal allowances, and
  the repository and project allowances where the boundary resolves them. Policy
  name `api`.
- **`expensive`**: ordinary admission plus the expensive allowances, permits and
  two-minute deadline described under
  [Incoming request budgets](#incoming-request-budgets-and-expensive-work). Policy
  name `expensive`.
- **`exempt`**: no admission. Only health probes that a probe handler serves: the
  GET of the Artifactory and Access ping, readiness and liveness operations (the
  router's health, readiness and liveness have no spec operation). A probe that is
  still a 501 stub, such as Distribution's ping or the Worker probes, stays
  `ordinary`: exempting it would drop its admission and rate headers, a changed
  answer with nothing gained. It becomes `exempt` with the code that serves it.
- **`recovery`**: the administrator recovery reserve described under
  [Temporary restrictions](#temporary-restrictions-and-administrative-recovery),
  for the extension's restriction and audit operations only. Policy name
  `recovery`. This fourth value is added because those operations bypass the shared
  allowances on purpose; declaring them `ordinary` or `expensive` would state an
  allowance they are never charged.

`x-cost-expensive-query` lists the query parameters whose presence makes an
`ordinary` operation expensive. Today that is `list` on the storage paths (the file
list, `GET /artifactory/api/storage/{repoKey}/{itemPath}?list`).

Every operation, stubs included, declares the class it was admitted under before
admission read the spec, as `expensiveRequest`, `HealthRequest` and
`APIRecoveryRequest` decided for its method and path. So taking the class from the
spec changes no policy name and no limit for any operation's own path.
Moving an operation to another class later changes its answers and needs its own
decision.

**Matching happens before routing.** `make generate` emits a table from the bundled
specs: method, route pattern, operation id, `x-cost`, `x-cost-expensive-query` and
whether the operation accepts a key. `limitPrincipal` finds the request in a chi
tree built from that table with `chi.Mux.Find`, so the lookup applies chi's own
precedence (a static segment beats a parameter) to the same patterns and the same
escaped path the router uses. A request that no spec pattern matches (hand-written
aliases, native protocol routes and unknown paths) falls back to path rules that
`expensiveRequest` keeps for those alone. Where a hand-written route serves a spec
operation under another pattern, the two must agree, and a test over every served
operation checks that they do. The hand-written item operations are looked up under
the pattern the router serves them with, whose repository key never matches `api`,
so an unknown two-segment API path such as `PUT /artifactory/api/foo` is no deploy
and keeps the path rules' class. One answer moved with the lookup: a request that
an operation addresses by name, whose last segment is a name that happens to be
`reindex` or `move-archives` (`GET /artifactory/api/repositories/reindex`), takes
the operation's `ordinary` class, where the path suffix rule made it `expensive`.
Its status and body are unchanged; its `RateLimit-Policy` names `api` instead of
`expensive`.

Charging the expensive lane after routing was rejected:

- Admission charges every counter in one transaction, all or none. Charging the
  expensive lane after routing would split that into two charges, so a request the
  expensive allowance refuses would already have spent its ordinary one. Keeping
  one charge would mean moving all principal admission after routing.
- Principal admission runs before routing today, after authentication and the
  restriction check, so it also covers answers no operation gives: 404, 405, and
  the Helm partial reindex that a middleware serves before routing. Admitting after
  routing would need admission in every route's chain, the not-found and
  method-not-allowed handlers included, to keep those answers charged and headed
  as they are.
- The expensive permit and the two-minute deadline must be in place before the
  handler starts.
- The key gate runs before routing and before authentication, and reads the same
  table.

The cost is a radix-tree lookup in each of the two admission steps, ingress and
principal, and a tree built once per process.

### Rate headers and Retry-After

`RateLimit` and `RateLimit-Policy` report the allowance admission evaluated for the
request, in draft-11 syntax. They are sent on:

- every response after principal admission, whatever its status: the operation's
  own answers, 404 and 405, a 501 stub, the 429 that refuses the request and the
  429 from the expensive permits (policy `api` or `expensive`);
- the 429 that refuses a request on the shared authentication allowance (policy
  `authentication`);
- every response on the recovery reserve, its refusals included (policy
  `recovery`).

They are not sent on:

- `exempt` probes;
- answers for which no allowance decision was reached: the key gate's 400, the 429
  for the process's 128 permits, and every 503 from admission storage (the last two
  with `Retry-After: 1`);
- answers decided after the authentication allowance admitted the request and
  before principal admission: authentication's 401, and the temporary
  restriction's 403 and 503.

Draft-11 lets a server send the fields on any status (§6) but warns against giving
untrusted parties capacity information, and notes that a 401 or 403 counted against
a quota lets one client probe another's traffic (§8.2). Before authentication the
only allowance is the shared one, so reporting it on every 401 would publish every
client's consumption to anyone. It appears only on its refusal, where the client
needs it, and shared exhaustion reports zero remaining. A permit or storage refusal
has no per-minute window to report.

`Retry-After`, in seconds and at least 1, is on every 429 and 503 that admission
writes (the rest of the window for a rate refusal, 1 otherwise) and on the replay's
503. An operation's own 503 that reports a transient condition carries it too. A 503
that reports configuration, such as a missing signing key, or a size bound is not a
hint to retry.

In the specs, the shared 429 response declares `Retry-After` as required and
`RateLimit` and `RateLimit-Policy` as optional (absent on the permit refusal). The
shared 503 response declares `Retry-After` as required and the two rate headers as
optional (present when the 503 follows admission, as the replay's does). No other
response declares the rate headers. Declaring a header on a response turns its
generated strict response type into a body-and-headers struct (compare
`AppendBuild503Response` in `internal/platform/gen/artifactory`), so every handler
would change, while the middleware, not the handler, writes these headers. The
header components' descriptions and this section state where else they appear.

### Paging classes and the next pointer

An operation returns a collection when its success body lists the things it names:
a top-level array, or the item array of an envelope (`users`, `results`, `tags`).
An array inside one resource (a user's groups, a role's actions, a build's modules)
or the report of an action (promotion messages, batch reports) does not make the
operation a collection. Every collection operation declares `x-pagination`, one of:

| Value | Meaning | Parameters | Next pointer |
| --- | --- | --- | --- |
| `cursor` | An opaque seek position (a keyset), not an offset. | Position `cursor` (Grid: `page_token`); size `limit`. | The envelope's field where it has one (Access v2 `cursor`, Grid `next_page_token`, the restriction audit `cursor`); otherwise `Link`. |
| `offset` | A zero-based index into a stable order. | Position `offset` (Distribution: `start_pos`; SCIM: the one-based `startIndex`); size `limit` (SCIM: `count`). | `Link`. |
| `page` | A page number in a stable order. | Position `page`; size `limit` or `pageSize`. | `Link`. |
| `native` | Paging that a wire format defines: a package protocol, or AQL's query text. | The protocol's own: Docker `n` and `last`, npm `from` and `size`, NuGet `skip` and `take`, Puppet `offset` and `limit`, AQL `.offset()` and `.limit()` in the body. | The protocol's own (Docker's `Link`, the Puppet Forge `next` field, which artifactweave answers under `pagination`) or none. Nothing is added. |
| `complete` | The whole collection in one answer, as the contract defines it. | None. | None. |

`x-pagination-parameters` names the operation's actual parameters as `position`
and `size`; either is left out where the contract has none (Distribution's
`start_pos` has no size). `x-pagination-next` names the pointer: `link`,
`body:<JSON Pointer>` (for example `body:/cursor`), or `none`, which only `native`
and `complete` may use. A size cap with no position, such as Distribution's edge
node `limit`, is not paging: the operation is `complete` and keeps the cap as JFrog
documents it. A timestamp that filters, such as the Worker history's `start`, is
not a position either.

- **JFrog's paging stays as JFrog documents it.** Its parameter names, defaults,
  bounds, headers and bodies do not change: Get Repositories keeps `limit`,
  `offset`, `X-Total-Count`, `X-Result-Offset` and `X-Result-Limit`
  ([#1367](https://github.com/weaveplatform/artifactweave/issues/1367)). A request
  without the paging parameters gets the whole list, as JFrog documents, and no next
  pointer.
- **The next pointer** is sent only when more items remain, so its absence marks
  the last page. Where the contract's envelope has a field for it, that field is
  used. Otherwise the answer carries `Link: <target>; rel="next"`
  ([RFC 8288 §3](https://www.rfc-editor.org/rfc/rfc8288.html#section-3)). The
  target is the path the client sent with its query, every parameter kept and only
  the position changed (offset plus size, the next page number, or the new cursor),
  the form the Docker catalog's link already takes. On a JFrog `offset` or `page`
  operation the `Link` is an extension (`LinkNext`, marked
  `x-artifactweave-extension`), an added header.
- **New lists, and the large collections artifactweave owns, page by `cursor`**
  with `cursor` and `limit`: default 100, maximum 1000, and 400 for a size outside
  1 to 1000 or a malformed cursor, as the restriction audit does. A cursor is a seek
  position bound to its filters and, where it matters, its caller. It is not a
  snapshot: rows added before it are not revisited, and deleted or newly hidden rows
  disappear (see [Access identity pagination](#access-identity-pagination)).
- **`offset` and `page` stay for JFrog operations that define them and for
  collections that stay small** (bounded by configuration, such as repositories or
  permission targets). The store applies the order, the caller's visibility and
  `OFFSET` and `LIMIT`, and reads one row past the page to know whether a next
  pointer is due, plus a count where the contract needs one.
- **`native` lists are classified, not rewritten.** Their bytes stay; their paging
  moves into the store behind the same bytes.
- **No list is collected and then sliced.** `cursor`, `offset` and `page` page in
  the store; `complete` answers, and `native` ones the protocol leaves unbounded,
  stream from store pages.

### Streaming complete lists

A streamed list is written from store pages and never built whole in memory, and
its bytes equal the collected form's: the same order, encoder settings and trailing
newline.

- The handler reads and filters the first store page before writing anything, so a
  failure there still answers a normal error with its status and envelope.
- It does not flush after each element. net/http then still sets `Content-Length`
  on an answer that fits its buffer and switches to chunked transfer only for a
  larger one, as it does for the collected form, so small answers keep their
  headers.
- Once the status and any bytes are sent, the status cannot change. A store
  failure or an expired deadline after that point ends the response by aborting the
  handler (`panic(http.ErrAbortHandler)`), as streamed downloads already do
  (`internal/artifacts/adapters/http/itemkit` and `pkgroute`). Over HTTP/1.1 the
  chunked body loses its final chunk; over HTTP/2 the stream is reset. The closing
  bracket or brace is never written, so even a body delimited by the connection's
  close is not valid JSON. Every client sees a broken body, never a shorter list
  that parses, and the cause is logged.
- No error element, trailer or other in-band marker is added: it would change the
  wire format, and JFrog's clients read none of them.

Keeping the whole list in memory so that the status could still change is what this
replaces. A partial 200 that parses is ruled out, as
[decision 0012](0012-search-and-aql.md) already rules it out for transitive AQL.

### Where the declarations live

The shared declarations live in one file, `api/src/components-shared.yaml`, a
fragment that holds only `components`, beside the service directories. It holds
the `IdempotencyKey` parameter (moved from `appendBuild` with its text), the
`TooManyRequests` (429) and `ServiceUnavailable` (503) responses, and the
`RateLimit`, `RateLimitPolicy`, `RetryAfter` and `LinkNext` headers, each marked
`x-artifactweave-extension: true`.

It sits there because `BundleAll` bundles each directory under `api/src` as a
service and skips files, so the file is never bundled as a service of its own, and
the scaffold, oracle and parity tools, which walk the service directories, never
meet it. A `shared/` directory would need an exception in each of them. Its name
follows the `components-*.yaml` fragments it behaves like.

The bundler (`internal/devtools/openapibundle`, `make bundle`):

- merges the file into each of the seven bundles as one more components fragment,
  under the existing rule that a second declaration of a component fails;
- adds `'429'` referencing `TooManyRequests` and `'503'` referencing
  `ServiceUnavailable` to every operation whose `x-cost` is not `exempt` and that
  does not declare that status itself;
- leaves an operation's own 429 or 503 as it is. An operation declares its own when
  its admission errors use another envelope than its spec's `ErrorResponse`: the
  paths that `errorProfileRules` (`internal/app/router.go`) gives the OCI registry,
  npm, pub, Swift, Go, Terraform, import and export or retention profile. Its own
  429 and 503 reference the shared headers, which the guard checks;
- adds no header to any other response, and `IdempotencyKey` and `LinkNext` to no
  operation: an operation references them itself once it honors them.

The shared responses' bodies reference `#/components/schemas/ErrorResponse`, which
each service's `components-common.yaml` defines as its own envelope, so every bundle
resolves them to its own error format. The parity check skips what is marked as an
extension, so the injected statuses and headers leave `api/parity-baseline.json` as
it is.

## Optional protected build append

`POST /artifactory/api/build/append/{buildName}/{buildNumber}` is the first
operation declared `key` and the first to declare `IdempotencyKey`, before build
delete and promotion (#1410); the [replay](#replay-for-keyed-operations) for every
keyed operation follows its design. The key is optional. Unkeyed append retains its append-on-every-call
behavior. The header value is quoted and contains 1–128 ASCII letters, digits,
`.`, `_`, `:` or `-`; a quoted UUID is recommended. Multiple header values,
malformed keys, and keys on operations that do not declare the parameter return 400
before mutation. Token issuance is explicitly unsupported (`none`,
`secret-response`): no token or other response secret is stored in a replay record.

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
return 400, and a deleted cursor row still permits continuation. In the terms of
[Paging classes](#paging-classes-and-the-next-pointer) both are `cursor`
operations whose next pointer is the envelope's `cursor` field.

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
`app.RouterConfig.RequestLimits`. `api.enabled=false` disables request budgeting,
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
the expensive classification. Each specified operation declares it as `x-cost`,
which admission reads from the generated operation table
([Cost classes](#cost-classes-and-how-admission-finds-them));
`expensiveRequest` in `internal/app/limits.go` decides it from the method and path
only for a request no spec operation addresses. They have
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

The rules for every operation need these checks as well:

- a guard over the seven bundled specs that fails when an operation lacks a class,
  a `natural` lacks its identifiers, a `none` lacks a listed reason, an operation
  that is not `exempt` lacks its 429, its 503 or their headers, a collection lacks
  `x-pagination`, a `cursor` lacks a next pointer, an operation declares
  `IdempotencyKey` without being `key`, or a served operation's `x-cost` disagrees
  with the admission it receives, each rule with a fixture that breaks it;
- for every served operation, with a budget of one, a second call answers 429 with
  `Retry-After`, `RateLimit` and `RateLimit-Policy`, and an `exempt` probe is
  never refused;
- for every keyed operation, a retry after a lost response performs one effect and
  answers as the first request did, and a changed body answers 422;
- following the next pointers of every paged list from its first page visits every
  item the caller may see exactly once, for an administrator, a partial reader and
  anonymous;
- a streamed list whose store fails after the first page ends in a body no JSON
  parser accepts.

The [operation ledger and source review](../api-reliability/README.md) cover all
seven specs and native aliases. The [generated-client retry example](../api-reliability/retrying.md)
shows bounded recovery. Focused tests alone do not establish the phase or release
gate; the PR retains the full CI result.
