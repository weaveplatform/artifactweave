# 0007: Remote repositories: proxy and cache

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 03](../03-repository-configuration-model.md#artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)).

## Context

A remote repository proxies an upstream registry and caches what it fetches. Artifactory
exposes the cache as `<repo>-cache` in storage and search responses, lets clients delete
and zap cache entries, and drives behaviour from a long list of per-repository settings:
`retrievalCachePeriodSecs`, `missedRetrievalCachePeriodSecs`, `assumedOfflinePeriodSecs`,
`storeArtifactsLocally`, `hardFail`, `offline`, `bypassHeadRequests`, `socketTimeoutMillis`,
`blockMismatchingMimeTypes`, `enableTokenAuthentication`, `clientTlsCertificate`,
`customHttpHeaders`, proxies and credentials. Each package format rewrites upstream
metadata so that clients keep talking to the proxy.

## Decision

**P9 clarification (2026-10-21, [#613](https://github.com/weaveplatform/artifactweave/issues/613)).**
The [remote retrieval contract](../../implementation/remote-retrieval-contract.md) is the
normative implementation table, field/ticket allocation and capture list. The original
7.161.32 research date and this ADR's Proposed status are unchanged. Current public
sources were rechecked; they are not live observations of that target.

`internal/remote` owns external HTTP transport and cache policy. The source boundary
also accommodates internal project repositories without URL-only identity or admin
loopback ([#614](https://github.com/weaveplatform/artifactweave/issues/614)). Format
adapters classify content; they do not each invent their own retry/cache policy.

| Item kind / mode | Retrieval decision |
|---|---|
| Unique binary or digest-addressed representation | Cache after complete validation; no TTL expiry and **no zap invalidation**. Explicit deletion or source-generation change permits a new fetch. |
| Mutable upstream metadata | Cache raw content separately from rewritten responses; expiry performs a bounded synchronous conditional refresh. A valid 304 advances validation time. |
| Explicitly zapped metadata | Expired, not deleted; next normal request attempts refresh first. Unavailable upstream may fall back to usable stale bytes unless hardFail or a stricter security/HTTP rule forbids it. |
| Locally derived metadata | Rebuild from authoritative inputs; never replace a remote version list with only the binaries cached locally. |
| Confirmed upstream 404 | Negative entry with its own expiry; other statuses, timeouts and denial are never cached as absence. |
| Cache alias or explicit offline | Serve existing complete cache content or 404; **zero upstream calls**, including refresh/probes/Smart side effects. |
| `storeArtifactsLocally=false` | Normal online reads use transient streaming; no new durable item, blob reference, index or negative cache. |
| Connection failure / hardFail | Bounded ten-attempt connection recovery and shared assumed-offline state; permitted stale fallback differs from hard failure. Exact scheduling/status choices remain [I] for #216. |

The public [Zap Cache reference](https://docs.jfrog.com/artifactory/reference/zapCache)
explicitly preserves immutable binaries. This replaces the original “delete or zap”
binary-refetch rule. The pinned provider describes waiting for metadata retrieval before
fallback; neither source establishes blanket asynchronous stale-while-refresh behavior.
The contract's R1–R14 and HTTP table define the precise precedence, including explicit
offline versus assumed-offline and denial versus temporary source failure.

HEAD precedes GET unless `bypassHeadRequests`; 405/501 preflight may fall back to GET,
whose complete body is authoritative. A HEAD alone never publishes a complete artifact.
Singleflight is scoped by repository incarnation, source/config generation, path,
representation, query and authorized intent. Atomic publication checks those identities
and the path invalidation epoch after I/O. Cache-only callers cannot join an upstream fill.

**Cache naming.** Cache entries are items with `cached = true` under the real repository
key. One function, `repokey.Resolve`, maps `<repo>-cache` to (`<repo>`, cached) on the way
in and renders `<repo>-cache` on the way out, so:

- AQL, storage info, file lists and search results show `<repo>-cache`;
- `GET`/`HEAD` on `/<repo>-cache/<path>` read cache only; `DELETE` and
  `api/zap` may target the alias, with distinct delete versus metadata-expiry effects;
- repository listings, configuration CRUD and permission targets never see `-cache`
  (`ANY REMOTE` and a remote's own grants cover its cache);
- deleting the remote deletes its cache;
- properties can be set on cache items.

**Format adapters.** Each format handler implements the upstream rewrite for its metadata
(npm tarball URLs, PyPI hrefs, Helm `urls`, NuGet `@id` values, Terraform download URLs,
Docker Hub token authentication and foreign layers) through the base-URL resolver.
Upstream validators belong to raw content; downstream validators describe emitted bytes.
Signed content is preserved. The contract maps each adapter to #623–#633 and assigns
remaining formats, Curation, replication and virtual composition to their named phases.

**Implemented source security (#618).** The shared resolver now loads sealed Basic/
Bearer credentials, sensitive headers, per-source cookies, client certificates and
HTTP(S) Basic proxies. It checks and pins destinations for redirects and metadata URLs,
with exact configured-private authority grants and unconditional known-metadata denial.
Cross-origin credentials require `allowAnyHostAuth` and HTTPS. These deliberate security
restrictions and the DNS-named HTTP proxy CONNECT requirement are recorded in the
[transport contract](../../implementation/remote-http-transport.md#resolved-source-security-618);
RC-6/#216 still owns observed Artifactory comparisons. NTLM/domain and proxy credential
redirect settings fail closed pending their P12 proxy/global-configuration implementation.
This is a shared foundation, not evidence that the native adapters or cache policy are
complete. Source HMAC revisions exclude retrieval policy, so TTL/offline changes do not
invalidate immutable bytes; publication separately fences current policy and credentials.

## Maintenance implementation checkpoint (#622)

[Remote cache maintenance](../../implementation/remote-cache-maintenance.md) records the
transactional zap/delete/restore fences and persistent unused-cleanup worker. Zap now
expires durable raw metadata and negatives without changing unique or mutable binary
items. Cleanup uses successful item downloads and retrieval-entry usage, protects
newer publications and recently used sidecars, preserves durable epoch tombstones,
and resumes bounded scan pages after restart. The hourly cadence and exact activity
clock are explicit implementation choices [I] for #216/P19. Raw upstream metadata,
derived views and package-index hooks remain separate. This checkpoint does not
change this ADR's status or claim native remote-format exits beyond their adapter work.

## Registry authentication implementation checkpoint (#623)

The [registry client](../../implementation/remote-registry-authentication.md) now provides
resolved Basic credentials, v2 probing, scoped Bearer exchange/reuse/expiry, bounded
401 retry and token-realm destination checks. Tokens remain isolated by source identity,
credential revision, image, service and scope, and never forward to CDN or other-image
redirects. Docker Hub has an exact documented realm credential exception; other
cross-origin realms remain anonymous unless explicitly enabled. These controlled
registry proofs do not activate native remote pulls (#624) or change this ADR status.
The strict realm, project-prefix, retry/expiry and cookie policies remain [I] for #216/P19.

## Rationale

A decision table makes the cache semantics testable one row at a time and gives the
acceptance features their structure. Storing cache entries as flagged items rather than as
a second repository keeps one item model while reproducing every place Artifactory shows
or hides the `-cache` name.

**Alternatives considered:**
- *A real `<repo>-cache` repository row*: leaks into listings and lets clients create or
  mutate it.
- *A separate cache store*: duplicates the item model and breaks AQL over cached items.

## Artifactory 7.x check

The decision stands against 7.161.32 as a plan for P9. The 7.x notes add inputs for P9:
- **Zap.** Zapping a remote cache needs Manage or Delete ([7.104.12](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-710412-self-managed)), as artifactweave's
  `api/zap` already checks.
- **Remote folder listing** is off by default ([7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed)).
- **Go and npm.** Go remotes serve only through a virtual repository (go-modules), and npm
  audit is on by default for npm remotes that support it (7.124.0).
- **Offline detection.** The "assumed offline" state was fixed repeatedly
  ([7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed), SaaS 7.174.0).
- **Version tokens** resolve in remote caches ([research 27 section 13](../27-maven.md#13-artifactory-7x-check)).

## Constraints

- Go remote repositories resolve only through a virtual repository, as in Artifactory.
- [Smart Remote synchronization](../../implementation/smart-remote.md) now implements
  authenticated discovery, initial/TTL properties, durable statistics and confirmed
  origin absence. Exact current-version wire details remain [I] under #216; P9 retains
  transitive lookup and P37 broader cross-vendor interoperability qualification.
- Singleflight is per process; two processes may fetch the same miss twice.

## Owned follow-up

P9 remains the owner of upstream fetch/cache, version search (#213) and initial Smart
Remote behavior. P28 supplies missing adapters within the existing ecosystems, and
P29–P33 add the other formats; their remote exits must use this transport and cache
contract. P37 owns advanced replication and external-peer interoperability, while
P35 addresses multi-process coordination. Stored remote fields are not proof of any
of these workflows.

The [capability roadmap](../../implementation/capability-roadmap.md) records the
phase boundaries under decision [0042](0042-capability-scope-and-supersession.md).
These assignments describe planned work, not current runtime support.

## Verification

- Every retrieval-contract row has fake-time/request-count, denial and fault coverage
  under #615/#620; every field has a concrete owner and required evidence. The captures
  RC-1–RC-8 remain [I] on #216/P19; fake upstreams do not establish vendor parity.
- `repokey.Resolve` tests over every endpoint family that may see a cache key.
- Acceptance features resolve each format through a remote against the fakes; nightly
  runs hit the real registries.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md), remote fields
- [04-package-protocols](../04-package-protocols.md), per-format upstream notes
- <https://docs.jfrog.com/artifactory/docs/remote-repositories>
