# Project-scoped remote repositories: full contract audit

Audited **2026-11-19** for [#487](https://github.com/weaveplatform/artifactweave/issues/487).
**The release boundary and discovery response schemas are documented. The public
sharing-mode mutation contract remains unresolved, so this evidence does not support a
claim of complete wire parity.** The issue still requires effective project-authorized
retrieval and the entire P9 handoff matrix, including revocation of warm and in-flight
reads; adding repository fields and discovery routes alone cannot close it.

## Current phase allocation (2026-11-19)

The maintainer moved #487 completion to the separate P19a phase after P19 observations.
P11 retains documented project administration and the private authority/role services
merged in #1730/#1731. This audit's dated source snapshot remains provenance; it does
not erase those later foundations or claim effective source retrieval. The remaining
sharing-mode and native/revocation contract is qualified in P19a.

## Evidence and scope

Checked against current JFrog documentation and the **7.173.8 self-managed / 7.173.4 SaaS
feature introduction**, with current self-managed release notes extending to 7.173.21.
The historical 7.161.32 compatibility label does not establish this feature's availability.
**[D]** identifies first-party documentation/OpenAPI; **[S]** identifies this project's
source or agreed issue/design contract; **[I]** identifies behavior chosen by this project
where the vendor contract is unsettled. No trial, live vendor response, new archive or
public GitHub write was used.

The project snapshot is `621523d38c1cda8b822fbb1b52a7f4b36e5f7b5d`, branch
`feat/project-scoped-remotes`. The current issue was read through read-only `gh` on the
audit date; its last update is 2026-10-27. Its P9 handoff is
[#614](https://github.com/weaveplatform/artifactweave/issues/614) and
[PR #642](https://github.com/weaveplatform/artifactweave/pull/642), merged 2026-10-21.
The local governing records are [the complete source design](../implementation/project-remote-sources.md),
[ADR0013](decisions/0013-projects-and-environments.md), the
[implemented source seam](../implementation/source-boundary.md), and the
[research policy](README.md#target-version-and-sources). ADR0013 remains **Proposed**.
No applicable `AGENTS.md` was found in the worktree or its ancestor directories. **[S]**

## Release boundary

| Deployment | Current primary evidence | Consequence |
| --- | --- | --- |
| SaaS | [7.173.4, released 2026-10-08](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas), names repository sharing with project-aware downloads and a gradual rollout beginning with that release. **[D]** | This identifies the announced introduction, not the activation date of every SaaS tenant. |
| Self-managed | [7.173.8, released 2026-10-26](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71738-self-managed), includes the feature and requires `artifactory.project.context.repo.sharing.mode=true`. **[D]** | The old handoff's “self-managed forthcoming” statement is superseded. |
| Current maintenance | [Self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-717321-self-managed) list 7.173.21 on 2026-11-17; detailed recent patch notes require authentication. **[D]** | This audit does not assert the absence of subsequent patch-specific fixes. |

The repository-create API's historical `Since: 2.3.0` and v2 configuration API's `Since:
6.12.0` describe those operations, not the newer project fields. The feature release
notes above are the relevant introduction evidence.
([Create](https://docs.jfrog.com/artifactory/reference/createrepository),
[v2 read](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2)). **[D]**

## Repository wire fields and coverage

Current raw Markdown/OpenAPI pages were fetched directly with the repository's documented
`artifactweave-specoracle` user agent; the selected Artifactory pages report
`updatedAt: 2026-11-12T12:42:36.000Z`. This reads published definitions, not vendor runtime
responses. The important schema leaves are:

| Field | Documented type and behavior |
| --- | --- |
| `projectScopedReadUpstream` | Boolean, default `false`; true selects a connection to a repository shared with the consumer's project. |
| `upstreamRepoKey` | String; mandatory in project mode and must be an eligible connectable repository. Create with an ineligible source is documented as 400. |
| `upstreamProjectKey` | String identifying the producer's owning project, returned for project-scoped remotes. The schema does **not** mark it `readOnly`, require input, or define supplied-value validation. |
| `url` | String with URI format; required for an ordinary remote, unnecessary in project mode and returned empty for a project-scoped remote. |

These are facts from `RemoteRepositoryConfiguration` and create prose, not evidence that
all input combinations have been observed. The schema's structural `required` list is
`key,rclass`; the conditional upstream requirement is expressed in property descriptions.
([Create definition](https://docs.jfrog.com/artifactory/reference/createrepository.md)). **[D]**

Switching an existing ordinary remote to project mode sets the boolean and upstream key;
switching back sets it false and supplies a URL. `rclass` cannot change.
([Update definition](https://docs.jfrog.com/artifactory/reference/updaterepositoryconfiguration.md)). **[D]**

| Surface to implement | Published coverage |
| --- | --- |
| `PUT /artifactory/api/repositories/{repoKey}` | Three fields in the remote request schema; conditional source validation above. [Create](https://docs.jfrog.com/artifactory/reference/createrepository.md). **[D]** |
| `POST /artifactory/api/repositories/{repoKey}` | Same remote fields and explicit mode conversion. [Update](https://docs.jfrog.com/artifactory/reference/updaterepositoryconfiguration.md). **[D]** |
| `GET /artifactory/api/repositories/{repoKey}` | Complete configuration includes the fields; non-admin partial configuration explicitly includes `upstreamRepoKey`. [Read](https://docs.jfrog.com/artifactory/reference/getrepositoryconfiguration.md). **[D]** |
| `GET /artifactory/api/v2/repositories/{repoKey}` | Same fields; project mode returns empty URL. [v2 read](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md). **[D]** |
| `GET /artifactory/api/v2/repositories/batch` | Remote configurations include the three fields. [Batch read](https://docs.jfrog.com/artifactory/reference/getbatchofrepositoriesbyname.md). **[D]** |
| `PUT` / `POST /artifactory/api/v2/repositories/batch` | The batch request's remote variant includes the three fields. [Batch create](https://docs.jfrog.com/artifactory/reference/createmultiplerepositories.md), [batch update](https://docs.jfrog.com/artifactory/reference/updatemultiplerepositories.md). **[D]** |
| `GET /artifactory/api/repositories` | Minimal list records add `upstreamRepoKey: string \| null`, with null documented for other repositories; the internal-remote example has `url: ""`. This list does not promise all three full-configuration fields. [List definition](https://docs.jfrog.com/artifactory/reference/getrepositoriesbytypeandproject.md). **[D]** |

**[I] Unsettled configuration choices:** derive the producer project from authoritative
ownership; do not let `upstreamProjectKey` grant access. Whether to reject a mismatching
supplied value, ignore it, or treat it as output-only needs an explicit compatibility
decision. The [P9 design](../implementation/project-remote-sources.md#p11-configuration-and-authorization)
proposes rejecting contradictory URL/HTTP credentials/proxy/header/certificate overrides
and clearing old transport secrets atomically during conversion; current documentation
does not settle redundant settings, defaulting or partial-update retention. Do not report
those proposed rules as observed vendor validation.

## Discovery operations: policy, shapes and statuses

| Operation | Scope and filters |
| --- | --- |
| `GET /artifactory/api/admin/projects/{projectKey}/connectableRepos` | Requires Platform Admin, or Project Admin **of the path project**. Optional `targetPackageType:string`, `keySearch:string`, `limit:integer`; limit bounds results. [Definition](https://docs.jfrog.com/artifactory/reference/getconnectablerepositories.md). **[D]** |
| `GET /artifactory/api/admin/repositories/{repoKey}/projectScopedConsumers` | Requires Platform Admin, or Project Admin of the project specified by optional `projectKey:string`; that query restricts consumer rows to the project. Optional `limit:integer`. [Definition](https://docs.jfrog.com/artifactory/reference/getprojectscopedconsumers.md). **[D]** |

Both produce JSON, accept the documented Basic/API-key/Bearer security alternatives,
and list **200, 400, 401, 403, 500, 503**. The earlier handoff omitted 500; do not copy
that omission. Neither currently documents 404 for these discovery operations.
([Connectable](https://docs.jfrog.com/artifactory/reference/getconnectablerepositories.md),
[consumers](https://docs.jfrog.com/artifactory/reference/getprojectscopedconsumers.md)). **[D]**

The published schema shapes, illustrated with audit values rather than captured responses:

```json
{"repos":[{"repoKey":"alpha-npm-source"}]}
```

`ConnectableReposResponse` is an object with `repos`, an array of objects containing
`repoKey:string`. It does not list package/project names, timestamps or a pagination
wrapper. Neither the outer field nor item properties has a `required` declaration.
([Connectable definition](https://docs.jfrog.com/artifactory/reference/getconnectablerepositories.md)). **[D]**

```json
{"consumers":[{"repoKey":"beta-npm-internal","projectKey":"beta","projectName":"Beta","created":1791969727963,"stages":["DEV"]}]}
```

`ProjectScopedConsumersResponse.consumers` items have `repoKey`, `projectKey`,
`projectName` strings; `created:integer/int64` is **when the repository was shared with
the project**, in epoch milliseconds; `stages:string[]` are the share's environments.
There are no `required` declarations on the response or item. Do not substitute consumer
repository creation time for share time.
([Consumers definition](https://docs.jfrog.com/artifactory/reference/getprojectscopedconsumers.md)). **[D]**

Both discovery definitions require the Artifactory error envelope
`{"errors":[{"status":403,"message":"..."}]}`: `errors` is required, and each entry
requires integer `status` and string `message`. An error schema's illustrative 404 does
not add 404 to an operation's status table.
([Connectable definition](https://docs.jfrog.com/artifactory/reference/getconnectablerepositories.md)). **[D]**

**[I] Policy interpretation and unresolved edges:** a project admin without a `projectKey`
consumer filter must not obtain platform-wide rows. Filters must follow authorization,
never widen it. The definitions give no limit bounds/default/order, substring/case rules
for `keySearch`, empty-filter semantics, or exact missing-project/repository status and
message. Implemented choices need explicit tests and inference tracking rather than
invented documentation claims. The source design requires denial tests for cross-project
filters and no metadata leakage.
([Project acceptance](../implementation/project-remote-sources.md#p11-completion-matrix-for-487)). **[S]**

## Sharing mode is distinct from the permission ceiling

The [sharing guide](https://docs.jfrog.com/projects/docs/share-repositories-across-multiple-projects)
documents mutually exclusive, repository-wide direct/project-aware methods; existing
shares are not migrated automatically. Project-aware requests retain user and consumer
project identity through an internal remote; direct consumer reads of the producer key
are refused. Effective rights intersect share ceiling, project actions and platform
permissions. Read-only users/shares may read existing consumer/producer cache; cold
external content returns not-found without upstream fill. Filling requires effective
Write and a writable share; shared-local deployment remains unavailable. Same-type sources
and virtual inclusion are supported; producer deletion breaks consumers. **[D]**

**All-project sharing is supported; “All Projects” is a connection context restriction.**
The guide explicitly allows project-aware auto-sharing to existing and future projects.
Its separate connection restriction forbids connecting from the All Projects context or
the producer's owner project. It does not exclude all-project shares from connectability.
([Current sharing guide, connecting and example sections](https://docs.jfrog.com/projects/docs/share-repositories-across-multiple-projects)). **[D]**

The [remote guide](https://docs.jfrog.com/artifactory/docs/remote-repositories) separately
distinguishes external HTTP/Smart remotes from same-platform internal sources, limits the
internal selector to same-package project-aware shares, and forbids native artifact
deployment to remotes. **[D]**

| Existing public API | Fresh definition establishes | Missing piece |
| --- | --- | --- |
| [Target share](https://docs.jfrog.com/projects/reference/sharerepositorywithtargetproject.md), `PUT /access/api/v1/projects/_/share/repositories/{repoName}/{targetProjectKey}` | Platform Admin; local/remote sources; optional boolean `readOnly` query; 204/400/401/403/404. **[D]** | No mode parameter, body, header or enum. |
| [All-project share](https://docs.jfrog.com/projects/reference/sharerepositorywithallprojects.md), `PUT /access/api/v1/projects/_/share/repositories/{repoName}` | Platform Admin; local/remote sources; 204/400/401/403/404. **[D]** | No mode field or `readOnly` parameter in this definition. |
| [Share status](https://docs.jfrog.com/projects/reference/getprojectrepositorystatus.md), `GET /access/api/v1/projects/_/repositories/{repoName}` | Platform Admin; `resource_name`, `assigned_to`, `environments`, `shared_with_projects`, `shared_with_all_projects`, `shared_read_only`. **[D]** | No mode property in the response. |

Those three Projects definitions report `updatedAt: 2026-04-05T17:23:57.000Z`, predating
the feature announcement. **[D]** Their silence cannot establish that no newer private or
UI API exists. Bounded searches of current JFrog documentation, the Projects reference
index, public release notes, JFrog-owned indexed client source for
`projectScopedReadUpstream`/`ProjectAware`, and the already available OSS 7.161.26 tree
found no substantiated mode mutation contract. The old OSS tree predates introduction;
its negative result provides no newer wire guarantee.

**Blocking conclusion:** `Direct` and `ProjectAware` are useful **internal design names**
in [the handoff](../implementation/project-remote-sources.md#p11-configuration-and-authorization),
not verified wire enum values. `readOnly`, `shared_read_only` and the existing
`SharedReadOnly` model represent permission ceilings, not the sharing method. Neither
`shareConfiguration` in a remote schema nor the self-managed enablement property is
evidence of a per-repository mode API. Do not invent a field, overload `readOnly`, or
claim full share/connect wire parity without a first-party contract or a clearly
documented, explicitly accepted deviation. **[S/I]**

Unsharing has a stronger published fact: connected internal remotes are denied, while
their configurations remain until deleted/reconfigured.
([Unshare guide](https://docs.jfrog.com/projects/docs/unshare-repositories-across-projects)). **[D]**
Ownership can be reassigned to another project, unassigning the old owner.
([Ownership guide](https://docs.jfrog.com/projects/docs/change-repository-ownership-from-project-to-project.md)). **[D]**
Warm-cache, race, same-key recreation and exact denial status behavior below remain
project requirements/inferences, rather than independently captured vendor results.

## What exists at the audited project commit

| Current source | Actual boundary |
| --- | --- |
| [Repository model](../../internal/artifacts/api/repo/repo.go), [validation](../../internal/artifacts/api/repo/validate.go), [parity baseline](../../api/parity-baseline.json) | Three config fields and the two operations remain gaps; remote validation still requires URL. The baseline also includes the list's `upstreamRepoKey`. A decoder accepting unknown fields proves no effective project behavior. **[S]** |
| [Project assignment](../../internal/access/core/model/project.go), [project store](../../internal/access/core/port/projects.go) | Ownership, share targets, all-project share and one read-only ceiling exist. No sharing method, project incarnation or durable grant revision is modeled there. **[S]** |
| [Durable source identity](../../internal/artifacts/core/model/source.go) | Repository incarnation, source generation and a `ProjectRepository` kind already exist. Project identity binding is future work. **[S]** |
| [SQL repository create](../../internal/artifacts/adapters/sql/content.go), [in-memory create](../../internal/artifacts/adapters/inmem/content.go) | Both allocate `NewRemoteSource` on creation for every repository class, including local producers. Existing repository incarnation records are available foundations; there is no equivalent project lifetime in the project model. **[S]** |
| [Source request/permit](../../internal/artifacts/core/remote/source/source.go), [authorization](../../internal/artifacts/core/remote/source/authority.go) | Request carries subject, access decider, original project and visited incarnations. Permit separates intent and network capability. Production authorization still accepts only `ExternalHTTP`; a project source fails unsupported before cache/network work. **[S]** |
| [Package source request construction](../../internal/artifacts/adapters/http/pkgroute/source.go) | `Request.Project` already comes from the configured repository's `ProjectKey`, not a client-selected project header. **[S]** |
| [Source seam](../implementation/source-boundary.md) | Existing source/credential/publication/admission fences are usable foundations, not project-sharing enforcement. Native metadata adapters and Docker have different route seams that P19a must compose. **[S]** |

The old design's “P8 baseline” source observations must not be copied as the current P9/P10
state. Conversely, the existence of P9 identity types must not be reported as effective
project remote support. **[S]**

## Full implementation and acceptance contract retained from #487

Every row below is required by the current [completion issue](https://github.com/weaveplatform/artifactweave/issues/487)
and [P9 completion matrix](../implementation/project-remote-sources.md#p11-completion-matrix-for-487). **[S]**
The **[I]** details are deliberate Artifactweave boundaries unless a newer vendor source
settles them. Endpoint-schema parity does not discharge these runtime obligations.

| Area | Required concrete proof |
| --- | --- |
| Share/connect/read/unshare | Two real projects; eligible producer; configured internal consumer; both discovery routes and every documented CRUD/batch/list shape; native read through consumer; attribution retains initiating subject and receiving project. Preserve producer-owner access. |
| Permissions and cold/warm cache | Independently vary Read/Write project roles, platform grants, token scope and share ceiling. Warm consumer cache and warm producer cache are distinct fixtures. Read-only access may resolve producer stored bytes without external network; cold external content must not fetch. Write on only one side cannot authorize fill. Even a writable internal remote cannot deploy to a shared local producer. |
| Two capabilities | **[I]** An explicit consumer cache alias stops at that consumer's cache. A normal internal read can retain `MayFetch` for producer resolution with `UpstreamNetworkAllowed=false`; an external remote producer then reads cache only. Nested/virtual traversal cannot widen either capability, and a restricted caller cannot borrow a privileged fill's newly fetched result. |
| Warm-cache revocation | After unshare, sharing-method change, caller/group/role/token restriction, deny GET, HEAD, ranges, conditional/304, versions/native metadata, direct cache aliases and listing/search/storage disclosure. Stale/offline/hardFail behavior, validators and prior positive/negative results cannot restore a grant. |
| In-flight revocation | Block producer fetch or waiter, revoke, then release. Revalidate before response headers/admission and cache publication; no old-grant response or commit into a changed generation. Close canceled/rejected bodies; release staged references; no SQL transaction spans waiting/network/streaming. |
| Lifetime and ownership changes | Delete/recreate producer **and either project** with the same key; change producer owner, consumer project or upstream; include delayed fills/background jobs, positive/negative caches and validators. **[I]** Durable incarnation bindings prevent automatic resurrection; explicit revalidation/rebinding advances generation. Source-item removal is a different freshness case from repository/grant deletion. |
| Invalid graph | Wrong type, missing source, wrong/owner/global consumer project, direct-only share, forbidden source and self-reference fail. **[I]** Multi-node and batch-introduced cycles fail atomically; runtime visited-incarnation/depth checks catch malformed imported graphs. Public docs establish local/remote sharing; virtual/federated producer eligibility is not established. |
| Administration | Exercise platform admin, matching and unrelated project admin, member, scoped token and anonymous caller. A consumer-list query without project scope requires platform authority; filters never disclose another project's rows. Include failure/status-table and principal-matrix evidence, not empty-success substitutes. |
| Credentials and identity | Internal services receive request authority, never an administrator loopback token or client-controlled project selector. Producer external credentials stay with its external source. Consumer requests cannot supply an alternate upstream URL or leak credentials in errors/audit/metadata. |
| Filtered metadata | **[I]** Reauthorize every disclosed descendant and segregate/filter cached representations by authority. Broad/narrow principals, native metadata, version queries and rewritten locations cannot reuse a broader successful representation or disclose producer URLs that bypass project checks. |
| Native/virtual consumers | At least two native ecosystems plus generic downloads exercise cold, warm and revoked behavior. Internal remote virtual membership must retain the original consumer context, visited chain and capability restrictions. Include Docker's separate source/delivery seam where relevant; do not assume one generic route proves every native path. |
| Redirect admission | **[I]** Initially stream internal-project bytes through the registry. Ordinary object-store/CDN bearer URLs cannot satisfy later unsharing checks. A future redirect must recheck current connection/policy on redemption; prove revocation before admission/signing and redemption, including warm cached paths. Already-delivered bytes cannot be recalled. |
| Completion evidence | Remove only implemented parity gaps; run generation/parity, status-table/principal matrices, meaningful success/denial/fault tests and native acceptance. Preserve the documented P11/P19a boundary and record new inferences for #216/P19. Do not close #487 on design or API-only completion. |

The service design also requires a source/grant revision changed transactionally with
revocation; conditional publication and fresh post-wait admission checks use that revision.
Logical cache/flight identity includes consumer incarnation, source kind/identity/generation,
canonical format resource/variant, and an authority partition for filtered representations.
Blob deduplication is not authorization. Raw and rewritten metadata have separate identities.
These implementation constraints are **[I]**, already owned by the
[cache/revocation handoff](../implementation/project-remote-sources.md#cache-identity-invalidation-and-revocation),
not newly inferred vendor behavior. P26/P27 scanning/Curation and later usage services
retain their phase owners; P11 preserves their receiving-project context without claiming
those products complete. **[S]**

## Remaining decisions and evidence needed

1. **Blocking for complete sharing wire parity:** obtain the first-party per-repository
   mode mutation/read contract, exact field and values, privileges, default and update
   semantics, including all-project sharing. The current public APIs do not supply it.
   Internal service/model work can proceed, but a guessed public mutation is not parity.
2. **Configuration semantics:** settle `upstreamProjectKey` input handling, mixed URL and
   internal settings, HTTP-secret cleanup and exact invalid/missing connection statuses.
   Output semantics, types, mode switching and ineligible-source create 400 are documented.
3. **Discovery edge semantics:** settle filter matching/order/limits, unknown-resource
   status and disconnected-consumer listing after unshare/delete. The ordinary shapes,
   scope rules and status sets are documented; these edges are not.
4. **Runtime vendor comparisons:** same-key repository/project recreation, ownership
   rebinding, cycle status/atomicity, direct-producer access exceptions for multi-project
   identities, revocation admission races, cache-alias denial, filtered representation
   isolation, producer-item deletion and redirect redemption remain **[I]** capture or
   deliberate stronger-boundary obligations. Keep the full acceptance matrix regardless.

The implementation can concretely use the documented repository schemas and discovery
operations, existing durable P9 source seam, local/remote producers and independent
read/network capabilities. Complete share-mode API parity needs the missing primary
contract; full #487 completion additionally needs all runtime evidence above. A successful
Artifactweave fixture proves its implementation, not a live JFrog response. **[S/I]**

## Retrieval provenance and validation

The fresh selected Markdown SHA-256 values are useful for locating a later documentation
change; proprietary OpenAPI blocks were not committed:

| Page | SHA-256 of fetched Markdown |
| --- | --- |
| `getconnectablerepositories.md` | `2030b26d3c9409968e7a30b20caba0144b3682b4ed7bbcea1faa4b34544cb628` |
| `getprojectscopedconsumers.md` | `880aca7a494a7046f627b2e43c5d8544c34860ac28a959fd95d87a7766c67e90` |
| `createrepository.md` | `cb26b16fe5bde4d253f73d99fb3c2694a0c961aa3355a99a53ef16c3a732b3cb` |
| `sharerepositorywithtargetproject.md` | `a2c9b1207c1b50728c55e4d4c1d18a9f03b1e005eb92e0156c0bb437109ea219` |
| `sharerepositorywithallprojects.md` | `3b6d728bb8a2edc49e7b900d436d67d724f48bf2955dca174a0f9027e312bc5a` |

Validation for this research-only change: cited local files and headings checked, complete
issue/design acceptance cross-checked, and whitespace checked. No runtime behavior or
production schema changed, so no runtime test claim is made.
