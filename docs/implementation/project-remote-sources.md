# Project-scoped remote sources

Status: **Proposed design**, researched on **2026-10-21** for
[#614](https://github.com/weaveplatform/artifactweave/issues/614). P9 provides the source
extension point; [#487](https://github.com/weaveplatform/artifactweave/issues/487) remains
open for P11 implementation and acceptance. This document implements no project API,
repository field or download behavior.

The shared retrieval contract is owned by
[#613](https://github.com/weaveplatform/artifactweave/issues/613), in
`docs/implementation/remote-retrieval-contract.md` and
[decision 0007](../research/decisions/0007-remote-repositories.md). Its cache/freshness
rules apply unless this document adds a stricter project authorization boundary.
[Decision 0013](../research/decisions/0013-projects-and-environments.md) owns projects;
the [capability roadmap](capability-roadmap.md) preserves the P9/P11 split.

## Evidence and version boundary

**[D]** means primary documentation; **[S]** means the current artifactweave source;
**[I]** marks our proposed behavior where the public contract does not settle it.
No live Artifactory instance was used. The ordinary compatibility baseline remains
7.161.32; this newer feature must not be presented as observed in that release.

| Primary source, checked 2026-10-21 | Contract established |
|---|---|
| [Remote repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories) | **[D]** HTTP upstreams and same-platform project repository upstreams are distinct. The internal selector requires a compatible package type and a project-aware share. Native clients cannot deploy artifacts to a remote repository. |
| [Sharing guide](https://docs.jfrog.com/projects/docs/share-repositories-across-multiple-projects) | **[D]** The feature is rolling out on SaaS; self-managed support is forthcoming. Internal requests retain user and receiving-project identity. Direct receiving-project reads of the producer key are refused. Share ceilings, project actions and platform permissions all constrain access. Read-only consumers use existing cache; filling a shared remote from its external upstream requires Write and a writable share. Shared local deployment is unavailable through an internal remote. Same-project and All Projects connections are unavailable. Source deletion breaks connections. |
| [Create repository](https://docs.jfrog.com/artifactory/reference/createrepository) and [update repository](https://docs.jfrog.com/artifactory/reference/updaterepositoryconfiguration) | **[D]** `projectScopedReadUpstream: true` selects `upstreamRepoKey`; `url` is then optional. A source absent from the connectable list is a 400. Switching back requires `projectScopedReadUpstream: false` and a URL. |
| [Connectable repositories](https://docs.jfrog.com/artifactory/reference/getconnectablerepositories) | **[D]** `GET /artifactory/api/admin/projects/{projectKey}/connectableRepos` requires that project's admin or a platform admin. Filters are `targetPackageType`, `keySearch`, `limit`. Documented responses: 200/400/401/403/503. |
| [Project-scoped consumers](https://docs.jfrog.com/artifactory/reference/getprojectscopedconsumers) | **[D]** `GET /artifactory/api/admin/repositories/{repoKey}/projectScopedConsumers` requires a platform admin, or a project admin when `projectKey` is supplied. That query filters consumers; `limit` bounds results. Documented responses: 200/400/401/403/503. |
| [Unsharing](https://docs.jfrog.com/projects/docs/unshare-repositories-across-projects) | **[D]** Unsharing denies connected internal remote requests. The consumer repository remains until deleted or reconfigured. |
| [Target share API](https://docs.jfrog.com/projects/reference/sharerepositorywithtargetproject), [all-project share API](https://docs.jfrog.com/projects/reference/sharerepositorywithallprojects) and [ownership changes](https://docs.jfrog.com/projects/docs/change-repository-ownership-from-project-to-project) | **[D]** Sharing requires platform administration and does not transfer ownership. The target API exposes `readOnly`; these references do not specify the new sharing-mode wire field. Ownership can be reassigned. |

The SaaS and self-managed release histories were searched for project-aware and
project-scoped remote introductions; no numbered introduction was verified. The sharing
guide's rollout statement is the available version evidence. P11 must capture the first
applicable build and its behavior; a repository-create endpoint's historical `Since`
version is not the introduction date of these fields.

## Current implementation and P9 boundary

The source inspection for this design is against the P8 baseline:

- **[S]** [repo.Config](../../pkg/repo/repo.go) has none of the three new fields;
  [validation](../../pkg/repo/validate.go) requires `url` for every remote. JSON decoding
  ignores unknown fields, including through [partial updates](../../internal/repos/repos.go),
  so a successful ordinary URL-based create/update does not prove these fields took
  effect. The [descriptor parser](../../internal/descriptor/parse.go) rejects unknown
  repository fields. This design changes neither behavior.
- **[S]** [parity-baseline.json](../../api/parity-baseline.json) retains the three field
  gaps across create/update/read/batch/list shapes and both missing admin operations.
  Those operations have no authored routes; do not describe them as implemented or as
  deliberate successful empty lists.
- **[S]** [ProjectStore](../../pkg/store/operations.go) and
  [RepositoryAssignment](../../pkg/types/project.go) already hold ownership, environments,
  shares and a read-only flag. They do not model project-aware sharing mode, connection
  identity or a monotonic authorization revision. [authz](../../internal/authz/authz.go)
  evaluates current identity/permission/token rules; that is not proof of P11 sharing
  administration or project-aware read enforcement.
- **[S]** [pkgroute.Source](../../internal/server/pkgroute/pkgroute.go) returns persisted
  items. #616 extends that seam for remote metadata, queries and streams without importing
  server packages into formats. Ordinary item and Docker routes must use the same source
  policy even though their routing adapters differ.

P9 registers only the external HTTP implementation. Neither a loopback URL nor an unknown
JSON field selects a project source. A future explicit `ProjectRepository` source with no
registered resolver must fail as unsupported before any cache or network access, never
fall back to HTTP. P9 does not expose the new fields, loosen the URL validation, add share
administration, remove parity exceptions, or close #487. This preserves current runtime
behavior while making the planned source representable.

## Source and request contract for #616

These are semantic records and call boundaries for implementation, not new public Go APIs
or JFrog wire fields. #616 may adapt their spelling to the existing `pkgroute.Source`
interface. They must live below protocol routing and must not embed `http.Request` or
`http.ResponseWriter` in format contracts.

| Record | Required contents and invariants |
|---|---|
| `RepositoryIncarnation` | Opaque durable identity allocated on creation. Reusing a repository key after deletion allocates a different identity. A key or configuration hash alone is insufficient. |
| `SourceIdentity` | Kind `ExternalHTTP` or `ProjectRepository`, consuming repository incarnation, opaque source identifier and `SourceGeneration`. The HTTP source identifier refers to the configured origin/authentication boundary; the project identifier binds the producer repository incarnation and owner-project incarnation. It is not a URL in disguise. |
| `SourceGeneration` | Monotonic revision or unique durable token changed on effective source/configuration changes. Credential rotation and changed proxy/certificate/header behavior participate even when their repository field names stay unchanged. Never include raw credentials in identifiers, logs or metric labels. |
| `ReadContext` | Authenticated subject/token scope, receiving repository and project identities, request resource, method/conditions, and a visited-incarnation chain. Derive project identity from the configured consuming repository; client headers/query strings cannot select it. Preserve the original receiving project through nested resolution. |
| `RetrievalIntent` | `CacheOnly` or `MayFetch`, derived by authorization and request routing. A cache alias forces `CacheOnly`: inspect that consumer's own cache without invoking any source. `MayFetch` allows source resolution subject to the separate network capability below. A caller cannot promote either capability. |
| `ReadPermit` | Request-bound authorization result: subject/scope identity, source identity, grant/policy revision, resolved resource, permitted intent, `UpstreamNetworkAllowed` and representation partition. It is not a persistent credential or a substitute for revalidation. |
| `SourceResult` | Persisted cached item or owned stream plus safe response metadata, actual source identity/generation, and representation identity. No-store responses need not manufacture an item. The consumer closes the stream on success, cancellation and errors. |

The narrow service boundary is:

```text
Resolve(config snapshot, repository incarnation) -> SourceIdentity, Source
Authorize(ReadContext, SourceIdentity)           -> ReadPermit | denied
Read(Source, ReadPermit, resource)               -> SourceResult | typed failure
Revalidate(ReadPermit, current state)           -> valid | denied | generation changed
PublishCache(SourceResult, expected generation) -> committed | superseded
```

`Resolve` must not perform network I/O or authorize a user. `Authorize` precedes all
positive/negative cache lookups and source operations. `Read` may cross a transport
boundary only when its permit allows it; an internal source calls repository services
with the permit, not the HTTP listener using an administrator token. `Revalidate` runs
after waits and before response admission, redirect signing and cache publication.
`PublishCache` is a conditional short transaction; no SQL transaction covers HTTP I/O,
waiting for a flight, or streaming bytes. Denials cannot become misses, offline state,
negative entries or stale-cache fallback.

The two capabilities are necessary: an explicit consumer cache alias may not consult
the producer, but a normal read-only internal request may use bytes already stored at
the producer. That normal request has `MayFetch` for internal resolution and
`UpstreamNetworkAllowed=false`. On reaching an external remote producer, the latter
becomes `CacheOnly` for that producer. Neither a nested source nor a virtual member may
turn it back on. Ordinary external remotes retain #616's Read-authorized system fill;
the extra Write/share ceiling applies to project-aware traversal.

HTTP credentials belong exclusively to the external source. The internal adapter accepts
no caller-supplied upstream URL or credential and never forwards a client Authorization
header to the producer's external server. A producer that is an external remote uses its
own configured credentials after the internal permission decision allows that fetch.
The request's audit record retains the initiating subject, receiving project, consumer
repository and producer; it never presents the fetch as an administrator's read.

## P11 configuration and authorization

**[I] Proposed validation**, except where the evidence table gives an explicit contract:

| Configuration case | Planned result |
|---|---|
| Ordinary HTTP remote | Existing URL and external-credential validation; internal source fields must not silently select another path. |
| Internal remote | Require `rclass=remote`, a real consumer project and a different producer project, `projectScopedReadUpstream=true`, and a source of the same package type returned by the connectability policy. Bind `upstreamRepoKey` to the current producer incarnation. |
| `upstreamProjectKey` | Derive the producer owner from authoritative assignment. If supplied, require agreement; do not trust it to grant access. Capture the exact input/output/default behavior before publishing the schema. |
| Conflicting HTTP/internal settings | Refuse an internal source with a nonempty URL or explicit HTTP credentials/proxy/header/certificate overrides. Clear old transport secrets atomically when converting HTTP to internal; require an explicit mode switch and new URL to convert back. Vendor treatment of redundant URL fields remains a capture case. |
| Source class | Initially local or remote, matching the sharing API's documented classes. A virtual/federated source must remain an explicit #487/P11 contract question, not an accidental consequence of recursive lookup. |
| Self-reference or cycle | Refuse before commit, including a cycle introduced by a batch update. Runtime resolution also checks the visited-incarnation chain and a bounded depth, so stale/imported graphs cannot recurse indefinitely. |
| Consumer project change | Revalidate the entire connection against the new project, rotate its source generation and reject an invalid new assignment atomically. Existing cache/grants cannot move implicitly with the key. |

P11 must extend the assignment model with a repository-wide sharing mode (`Direct` or
`ProjectAware`), a share permission ceiling and a durable revision. Keep the set of
receiving projects, including all-project shares, distinct from the producer owner.
Do not infer the new mode from `SharedReadOnly`: direct read-only sharing already exists
as a separate concept. The exact new wire field is unresolved; the model names here are
internal only. Connection rows bind both repository incarnations and project identities
and support reverse lookup for `projectScopedConsumers`.

For each request, the P11 authorizer performs these checks in order:

1. Resolve the consuming repository incarnation, project and source binding; authenticate
   the caller. Apply repository availability, include/exclude patterns and consumer Read
   rights under its project roles, platform permissions and token scope.
2. Resolve the current producer incarnation and owner. Require a current project-aware
   share to the consuming project. Apply the share ceiling and source path restrictions;
   an unrelated producer-wide user grant must not bypass receiving-project restrictions.
   The existing evaluator needs a project/share-aware extension, not an admin `Decider`.
3. Compute both capabilities. **[I]** An explicit cache-only request stops at the consumer
   cache. A normal internal read may resolve a readable local producer's stored bytes or
   a remote producer's existing cache. External network access additionally requires
   effective consumer Write and a writable share. Every nested share may narrow that
   capability. The permission permits system cache population only; PUT/deploy remains
   disallowed through the internal remote.
4. Apply later receiving-project governance hooks (P26/P27) before disclosing cached or
   fetched content. Their absence remains visible; P11 does not claim scanning or Curation
   complete. Preserve the receiving context needed for usage attribution and P40 services.

These rules also apply to HEAD, conditional 304 responses, range reads, native metadata
and version queries, cache aliases, listing/search/storage views, and signed download
URLs. **[I]** A receiving-project direct read of the producer key is denied even when the
user can read its consumer remote. Producer-owner reads remain ordinary authorized reads;
the rule must not blanket-disable the producer for its own project.

## Cache identity, invalidation and revocation

**[I]** Retain the public `<consumer>-cache` alias and cached-item model. The logical cache
entry, negative result, validator, rewrite output and in-flight fetch additionally bind:

```text
consumer repository incarnation
+ SourceIdentity kind / identifier / SourceGeneration
+ canonical format resource (path or query, response variant)
+ authorization representation partition where content is permission-filtered
```

The blob store may deduplicate identical bytes across projects, but a checksum is not an
authorization grant. Raw origin metadata and rewritten client metadata have distinct
identities. Bind rewritten URLs to the consumer/base-URL variant; never return a producer
URL that lets the client bypass project checks. Permission-filtered metadata must either
be filtered after reading a common raw entry or partitioned by a stable authorization
fingerprint. Never share a successful body merely because two requests named one path.

Positive and negative entries from one source generation cannot satisfy a normal read
from another. Cached old-source content may remain physically present for cleanup, but
the internal connection's authorization still gates even explicit cache-only reads.
Invalidate negative entries and validators on reconnection/source changes; an old miss
cannot hide newly authorized content. Zap follows #613 and never substitutes for grant
revocation. A `CacheOnly` request cannot join a `MayFetch` flight or acquire that flight's
newly fetched result without a fresh authorization decision. Include the network
capability in internal flight compatibility too: a read-only caller cannot piggyback
on a concurrent caller's authorized external fill.

| State change | Required effect, including warm caches |
|---|---|
| Unshare or switch from project-aware to direct sharing | Invalidate the connection grant revision. Deny subsequent consumer/cache-alias responses; retain the remote configuration for repair. No stale/offline fallback. |
| User/group/project role or token restriction changes | Re-evaluate the caller before admission; a cache hit never carries the rights of whoever filled it. A Read-to-no-Read change denies; Write-to-Read narrows subsequent fetch intent. |
| Producer deleted and recreated under the same key | The old connection remains disconnected. New incarnation requires explicit validation/rebinding and a new source generation; no automatic resurrection of shares or cached visibility. |
| Producer ownership changes or either project is deleted/recreated | Treat the bound project identity as changed, invalidate authorization and require connection revalidation. A same-spelled project is not the old project. |
| Consumer source or project changes | Conditionally publish only against the old expected generation; old fills/refreshes cannot overwrite the new connection's cache. |
| Producer item removed but repository/share survives | Distinct from revocation: normal freshness rules determine cached-content reuse. Source item absence alone does not grant new rights or erase consumer cache. Capture exact vendor behavior under #487. |

Revocation is a database state change, not a best-effort purge job. P11 must update the
share/connection revision in the same transaction as the mutation. Revalidation reads
that revision and current authorization; background jobs and flight waiters must not
reuse an old permit. Cache commit uses a conditional generation/revision check. If a
source binding changed, discard the staged result and release its temporary references.

The response admission point is the final authorization check before headers or a signed
URL are handed to the client. A revocation committed before that point denies the request;
bytes already delivered cannot be recalled. Do not hold a database transaction while
streaming. **[I]** Internal project reads must stream through the registry initially:
ordinary object-store/CDN bearer URLs cannot enforce later unsharing. A future redirect
needs a capability that rechecks the connection and policy on every redemption, and must
remain tracked under #487 until that proof exists.

## P11 completion matrix for #487

Use producer project `alpha`, consumer project `beta`, a same-format producer repository,
and `beta-source` as the internal remote. A second consumer with different permissions
must not inherit either the first consumer's cache identity or grant. Prove each row in
store/service tests and native client acceptance where applicable; fake tests do not
settle undocumented Artifactory statuses.

| Case | Required proof |
|---|---|
| Share, connect and read | Both discovery endpoints report the correct permitted rows; the three fields round-trip through all CRUD/batch/list shapes; native reads resolve through `beta-source` and retain beta attribution. |
| Read-only share or member | Warm consumer/source cache succeeds. A cold external object gives not-found without network I/O. Granting Write on only one side still cannot fetch; granting it on both permits system fill but no client deploy. |
| Unshare after cache fill | GET, HEAD, range, conditional, metadata, version query, direct cache alias and discovery/search/storage paths disclose no content or metadata to beta after revocation. Offline/hardFail/stale behavior cannot override denial. Alpha's own authorized read still works. |
| Revocation while work is blocked | Pause upstream or a flight waiter, revoke, then release. No response is admitted under the old grant and no cache entry is committed into a new generation. Cancellation closes streams; no transaction spans the block. |
| Source/consumer/project mutation | Delete/recreate the producer or either project; change producer ownership, consumer project or source. Old cache, negative entries, validators and delayed jobs cannot bind to the successor. |
| Invalid connection | Wrong type/project, direct-share-only source, missing source, self-reference and multi-repository/batch cycles fail atomically. Runtime traversal catches malformed graphs. |
| Admin boundaries | Project admin can query only its documented scope; a consumer-list query without project scope requires platform admin. Unauthorized filters reveal no other project's repositories. |
| Credentials and representation | No loopback/admin credential, producer credential leakage or rewritten producer URL. Narrow and broad principals cannot share filtered metadata incorrectly. |
| Native and virtual paths | At least two native ecosystems plus a generic download prove warm/cold/revoked behavior; P10 virtual membership uses the same permit and visited chain. P9 must not enable unfinished virtual behavior accidentally. |
| Parity closure | Remove only gaps whose fields/routes and effective behavior pass; run generation/parity/status/principal checks. #487 closes only with the P11 matrix, not this design. |

## Remaining capture questions and handoff

P11/#487 owns bounded research for the sharing-mode API field, discovery response schemas,
first enabled vendor build, accepted producer classes, redundant HTTP settings,
`upstreamProjectKey` mutability/defaulting and exact failure statuses. Do not guess a wire
field from an internal model name. Request source-backed fixtures before marking those
contracts implemented.

For [#216](https://github.com/weaveplatform/artifactweave/issues/216)/P19, capture the **[I]**
choices above: cache-only denial after unshare, incarnation/rebind behavior, authorization
race/admission semantics, filtered metadata partitioning, producer item deletion versus
repository deletion, cycle rejection, direct-source denial exceptions and redirect
revocation. Document any deliberate stronger boundary as a deviation; do not weaken
revocation to mimic a vendor cache bypass. Root will add this inventory and the P9/P11
split to #487/#216 when publishing the design PR; this document does not claim those
GitHub updates have occurred.

The immediate P9 exit is narrower: #616 can register an HTTP source behind the tagged
identity, pass authorization-derived intent/context, fence cache publication by source
generation, and reject an unsupported project kind. It must not depend on a project-mode
wire contract or project administration being implemented early.

Implementation ownership stays explicit: #616 defines and tests the injectable source
boundary without enabling production HTTP retrieval; #617/#618 provide transport and
credential isolation; #619 owns durable repository/source identities and conditional
cache publication; #620 supplies cache policy; #621 enables the generic remote route.
Until those pieces are connected, existing runtime class gates remain. P10 alone enables
virtual traversal, and P11 supplies project/share identity revisions and enforcement.

The implemented P9 injection API and its activation gates are documented in
[Remote source read boundary](source-boundary.md).
