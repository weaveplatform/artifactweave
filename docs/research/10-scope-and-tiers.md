# 10: Scope and availability

Research baseline: **2026-10-12**, extended **2026-10-13** for the enterprise products;
7.x findings checked against Artifactory 7.161.32 on **2026-10-18**
([#378](https://github.com/weaveplatform/artifactweave/issues/378)). Availability and
phase ownership reviewed **2026-10-21** after merged P8.

**All identified capabilities are in scope unless a newer capability supersedes them.**
Decision [0042](decisions/0042-capability-scope-and-supersession.md) replaces the old
permanent implement/shim/stub/ignore tiers. The
[capability roadmap](../implementation/capability-roadmap.md) owns the full inventory,
phase dependencies and supersession register; the
[implementation plan](../implementation/plan.md#build-status) records delivery.
P0–P8 are complete against their recorded exits. P9–P41 are planned, not delivered.

## Tiers

These are availability states, not permanent scope cuts:

| State | Meaning |
|---|---|
| Implemented | Behavior has a concrete handler and relevant test/client evidence; any remaining boundary is named |
| Partial | Some behavior works, but configuration acceptance or generic byte storage does not prove the rest |
| Generated stub | An authored operation inherits a 501 handler until its owning phase implements it |
| Prefix stub | A product prefix returns 501 without a complete operation-level contract |
| Missing route | No handler; normally 404 after authentication, or 405 where the path has another method |
| Superseded interface | A named replacement and migration evidence permit retiring that interface; the underlying capability remains covered |

Status codes below assume a valid, authenticated request where required. Authentication
and request binding can reject a request before it reaches a stub. Current evidence is
[`server.go`](../../internal/server/server.go),
[`system.go`](../../internal/server/system.go),
[`stub_test.go`](../../internal/server/stub_test.go), the format status registrations and
native acceptance features. A generated schema or a stored configuration field alone
is not runtime support. Client contracts are recorded in
[research 08](08-terraform-providers.md) and [09](09-jfrog-cli-compatibility.md).

## Error envelopes by surface

| Surface | Envelope |
|---|---|
| Artifactory REST (`/artifactory/api/...`) | `{"errors":[{"status":<int>,"message":"<string>"}]}` |
| Access, Projects (`/access/...`) | `{"errors":[{"code":"<string>","message":"<string>"}]}` |
| Event (`/event/...`) | as Access |
| Distribution (`/distribution/...`) | `{"status_code":<int>,"message":"<string>","detail":"<string>","errors":[{"status":<int>,"message":"<string>"}]}` |
| Cold-storage retention (`/api/retention/...`) | the Artifactory envelope plus top-level `"error":"<status text>"` and `"message":"<string>"` |
| Import and export (`/api/import/...`, `/api/export/...`, `/api/artifactexport/...`) | `{"errors":[{"status":<int>,"message":"<string>"}],"error":"<string>"}`; `error` carries the message |
| Platform federation (`/pfed/...`) | `{"code":<int>,"message":"<string>"}`; `code` is the HTTP status |
| Grid (`/grid/...`) | `{"code":"<CODE>","message":"<string>"}`, with `details` when set |
| Workers (`/worker/...`) | as Access *(JFrog documents none; unverified)* |
| Xray (`/xray/...`) | `{"error":"<string>"}` |
| OCI distribution (`/v2/...`) | `{"errors":[{"code":"<CODE>","message":"<string>","detail":<any>}]}` |
| npm (`/api/npm/...`) | `{"error":"<string>"}`, with `reason` on publish conflicts |
| Terraform registry (`/api/terraform/...`: the module and provider registries and the network mirror) | `{"errors":["<string>"]}` ([decision 0040](decisions/0040-terraform.md)) |
| Go proxy (`/api/go/...`) | plain text, the only form whose body the go command shows ([decision 0035](decisions/0035-go-modules.md)) |
| PyPI (`/api/pypi/...`) | the Artifactory envelope, as every 7.x capture there is ([research 28](28-pypi.md), [decision 0037](decisions/0037-pypi.md)) |
| NuGet (`/api/nuget/...`) | the Artifactory envelope, as 7.x answers its NuGet routes; `NuGet.Protocol` reads only the status ([research 29 sections 7 and 9](29-nuget.md#7-push-delete-and-relist)) |
| Helm (`/api/helm/...`) | the Artifactory envelope; `helm` reads only the status ([decision 0039](decisions/0039-helm.md)) |

The JFrog operations beside a registry with an envelope of its own answer in the Artifactory
envelope: npm's login and reindex (RTFACT-30181, [decision 0034](decisions/0034-npm.md)) and
Terraform's reindex **[I]**. PyPI and Helm were plain text here until
[#532](https://github.com/weaveplatform/artifactweave/issues/532).

The table is implemented once in `internal/server/apierror` and tested per surface.

## Product families

| Product or capability | Current availability after P8 | Phase ownership and client completion boundary |
|---|---|---|
| Core repository/item/storage APIs, properties, copy/move/trash/GC and supported searches | Implemented, with explicitly recorded search/build omissions | P5 evidence; P9 upstream searches; P28 remaining AQL/search/build APIs |
| Docker/OCI, npm, Go, Maven/Gradle/Ivy/SBT, PyPI, NuGet, Helm and Terraform registry local protocols | Implemented local workflows and maintenance operations; remote/virtual protocol resolution remains incomplete | P6–P8 evidence; P9 remote, P10 virtual, P28 remaining client/protocol gaps |
| Terraform backend (`terraformbackend`) | Implemented state API, locks and native Terraform workflows | P8; [research 32](32-terraform-backend.md); shared browser approval remains P21/#262 |
| Other package ecosystems | Repository configuration and generic byte storage can work; dedicated metadata and native protocols are absent. Some maintenance APIs are generated 501s, not universally 404 | P29 Linux; P30 languages; P31 native build/plugins; P32 data/ML; P33 automation/VCS. Every type/class is listed in the capability roadmap |
| Docker Registry V1 | Transport absent; V1 configuration round-trips and `/api/docker/<repo>/v1/auth` still serves the Docker configuration helper | V2/OCI is the named replacement; P28 owns migration and port-per-repository routing |
| Legacy security shims and Access users/groups/permissions/tokens/roles/global environments | Implemented, including CI OIDC exchange; API-key creation answers 400 | P4; reference/identity tokens replace legacy key issuance. P21 human authentication, P22 provisioning |
| Projects, roles/membership and repository attach/share (`/access/api/v1/projects`) | Generated 501s; global environments and repository `projectKey` persistence are not project management | P11; real `jfrog/project` apply/plan/destroy and effective authorization; project-scoped upstream consumption spans P9/P11 |
| Webhook subscriptions and delivery (`/event/api/v1`) | Generated 501s; no delivered event service | P11 core domains; P15/P16/P23–P27 product events. P40 owns estate/pipeline integration events; accepted domains cannot remain permanently silent |
| Package cleanup policy execution | Generated 501s; direct trash/GC and build retention are separate implemented operations | P11; real policy runs and recovery, not only Terraform CRUD |
| Build cleanup and release-bundle cleanup policies | Generated 501s | P24, including `artifactory_release_bundle_v2_cleanup_policy` |
| Build-info and AQL | Publishing/promotion/retention and supported domains work; Builds Diff returns 501; `item.infos` returns 400; release domains are not yet served | P15 release domains; P28 Builds Diff, property-change metadata and remaining query/index/archive operations |
| System probes/version/license identity/storage info, reverse-proxy configuration read, seven-section YAML PATCH | Implemented subset; usage and JFConnect metrics sinks discard bodies and return 200 | P12 full descriptor/system configuration; P18 basic observability; the sink is not account/entitlement support |
| Debug loggers/locks, live logs, support bundles, background tasks/system info and metrics | Generated or handwritten 501s; debug loggers/locks are not implemented | P18 shared basic diagnostics; P38 advanced administration, including `jf rt logs` |
| XML descriptor POST, Artifactory/Access import/export, scheduled backup/restore and checksum migration | Unimplemented contracts/stubs | P36; validated migration and recovery, not an indefinite cut |
| LDAP | Generated 501s; no LDAP-backed login yet | P12 directory bind/group sync and native provider acceptance |
| SAML, Crowd, HTTP-SSO and human OIDC/OAuth/session/MFA flows | SAML/Crowd/HTTP-SSO generated 501s; workload OIDC exchange does not supply browser identity | P21; real identity-provider login and effective access |
| SCIM users/groups | Generated 501s | P22; provisioning/deactivation/group changes must affect access and token/session revocation |
| Single push/pull, multi-push and event replication | Generated management stubs; repository definitions are not replication | P11 single replication; P37 multi-push/events/checksum replication/Smart Remotes and vendor interoperability |
| Federated repositories and peer trust | Repository definitions and local content work; synchronization/pairing service behavior remains unimplemented | P13 artifactweave peers; P37 cross-vendor/RTFS contracts and interoperability |
| Access Federation, `/pfed` and `/grid` | Generated 501s | P14 artifactweave topology/identity; P27 Curation entity synchronization; P40 external estate integration |
| Distribution and release bundles v1 | Generated 501s; `releasebundles` has no dedicated handler yet | P15 signed bundle workflows; P26 scan trigger; P37 vendor Edge interoperability |
| Cold storage/archive/restore | Configuration/model exists; policy execution and restore remain unimplemented | P16 internal cold tier; P37 separate cold-instance and vendor contracts |
| Workers | Generated 501s; no TypeScript execution runtime yet | P17 goja/esbuild runtime; P27 runtime-workload event integration, P40 other integration contracts |
| Evidence (`/evidence/*`) | Prefix 501; package signatures/referrers are not the full Evidence service | P23 provenance/signatures/subject binding; P26 OneModel queries |
| Lifecycle/RBv2 (`/lifecycle/*`) and Access stages/lifecycle | Lifecycle prefix 501; `/access/api/v2/stages` and `/access/api/v2/lifecycle` have no routes (404) | P24 creation/promotion/distribution/cleanup and Terraform resources; compatibility/migration shares AppTrust foundations |
| AppTrust and Unified Policy | `/apptrust/*` prefix 501; `/unifiedpolicy/*` absent (404) | P25 applications, versions, package binding, policies/evaluations and enforced gates |
| Xray/security scanning/SBOM/vulnerability and license intelligence | `/xray/*` prefix 501; `xrayIndex` persists but causes no scan | P26; real `jf audit`, `jf scan`, build/release scans, policies/reports and explicit intelligence-source contracts |
| Catalog and OneModel GraphQL | No routes or complete service schemas (404) | P26 Catalog/gateway and package/security queries; P23–P25 provide evidence/governance data; P14/P40 topology. GraphQL is not covered by REST parity alone |
| Advanced Security and Curation | Xray prefix 501; Curation settings APIs are generated stubs and repository flags are stored-only | P27 analysis engines, admission/audit mode, waivers and federated policies; live intelligence/engine access remains a tracked dependency |
| Package Traffic Controller (PTC) | Package-reroute service is absent (404) | P27 after P9/P10: remote-repository routing and Curation enforcement; security-edge integration and native-client proof |
| Mission Control/JPDs/license buckets (`/mc/*`) | Explicit 404; absent from authored API coverage | P40 estate and licensing contracts; authorized vendor integration or clearly identified native analogue |
| MyJFrog/account/network-control/usage APIs | Absent routes; portal/entitlement hosts and authentication differ from the registry | P40 account, allowlist/private-link/domain/usage contracts and `jfrog/myjfrog` coverage; no fabricated upstream success |
| JFConnect/JES entitlement and consumption | Only the CLI metrics discard sink works; other routes absent | P40 online/offline registration, issuer/expiry checks and usage; genuine vendor integration distinguished from a native entitlement analogue |
| SaaS bridges and service pairing | Bridge routes and tunnel runtime absent | P40, with P13/P35/P37: outbound tunnel, trust, lifecycle, HA/recovery and qualified vendor integration |
| Pipelines and legacy Integration service | No implementation; product retirement alone does not establish a complete successor | P40 capability/consumer research, named replacement/migration or compatible execution/integration contract |
| AWS IAM mapping, Vault and cloud log shipping | Generated 501s; `vaultKey` without a private key is rejected | P38 external integration and native provider proof |
| Standalone signed URLs/keys, application metrics, CA/root certificate and encryption-key administration | Unimplemented operations; CDN/direct-storage redirect and ordinary keypair support already work | P38 for these separate APIs; P23 signing/provenance contracts |
| MySQL, HA and general web console | SQLite/PostgreSQL and single-process runtime work; these broader capabilities do not | P34 database portability; P35 HA; P39 complete console; P21 only supplies login foundations |

P20 reconciles every missing route, stub, stored-only feature and client resource into
an operation-level ledger and adds the unmapped-gap check. On **2026-10-21**,
`api/parity-baseline.json` records **63 missing operations and 8 divergent operations**:
61 missing Mission Control/MyJFrog/bridge/JFConnect operations, two project-scoped
repository operations, and repository upstream-project fields in eight operation shapes.
The 22-index oracle does not yet inventory all Security, Governance or GraphQL contracts.
These are tracked gaps, not proof of complete platform coverage.

## Artifactory 7.x check

Checked on 2026-10-17 against the self-managed release notes up to 7.161.32
(https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases), the legacy notes
for 7.0 to 7.83 (https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf) and the deprecations page
(https://docs.jfrog.com/releases/docs/artifactory-deprecations). This dated check supplied inputs to the scope review; it is not a current exclusion
list. CDN redirect has been implemented since P4b. The findings recorded then were:

| Product | 7.x | Source |
|---|---|---|
| Federated repositories | Served by the standalone Artifactory Federation Service (RTFS) at `/rtfs` from 7.111.12. Four status operations are legacy-only, and new features such as unidirectional sync are RTFS-only ([research 16](16-enterprise-products.md#4-federated-repositories)) | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed |
| Cold storage and archive | Smart archiving also serves a self-hosted active instance (hybrid, Enterprise+, from 7.117.14) | https://docs.jfrog.com/administration/docs/archive.md |
| Platform Federation and Grid | The Grid product syncs projects and identity on SaaS only (7.154.0); the documented `/pfed` API still carries Curation entity types | https://docs.jfrog.com/administration/docs/jfrog-grid.md |
| Workers | Scheduled workers from 7.104.5; repository events on self-managed from 7.161.15 | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed, https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed |
| Distribution and release bundles v1 | Still available; the v1 Replicator was deprecated in 7.84.10 | https://docs.jfrog.com/releases/docs/artifactory-deprecations |
| Pipelines | Confirmed sunset: end of life on 1 June 2025 | https://releases.jfrog.io/artifactory/legacydocs/JFrog-Pipelines-End-of-Life-Jun-1-2025.pdf |
| Mission Control | Its user plugins were removed in 7.117; Access Federation still needs Mission Control and a Circle of Trust | https://docs.jfrog.com/releases/docs/artifactory-deprecations, https://docs.jfrog.com/administration/docs/access-federation.md |

**Services without a tier in the original audit.** Topology/OneModel, the Integration
service and Package Traffic Controller were originally left unresolved because client
usage was unknown. They now have owners: OneModel/Catalog P26, PTC P27, and legacy
integration/estate contracts P40. Current primary documentation exposes
[OneModel GraphQL](https://docs.jfrog.com/integrations/docs/jfrog-one-model-graphql),
[Catalog queries and mutations](https://docs.jfrog.com/integrations/docs/catalog-onemodel-graphql)
and [PTC routing](https://docs.jfrog.com/security/docs/package-traffic-controller).
PTC targets a remote repository; it does not forward developer credentials, and virtual
or Smart Remote targets are not part of its documented contract.

**Envelopes and legacy endpoints.** The audit of 2026-10-18
([#378](https://github.com/weaveplatform/artifactweave/issues/378)) settled two rows of the
envelope table:
- The `/pfed` error envelope carries `code` as the integer HTTP status, on every documented
  `/pfed` operation (9 in the fetch of 2026-10-19); only `/grid` uses a string `code`
  ([Add Follower JPD](https://docs.jfrog.com/administration/reference/addfollowerjpd.md),
  `ErrorResponse`: `"code": {"type": "integer", "description": "HTTP status code"}`).
- On `/api/import`, `/api/export` and `/api/artifactexport`, `error` is the "simple error
  message (legacy format)" beside `errors[]`, with no top-level `message`
  ([Export Repository with Metadata](https://docs.jfrog.com/administration/reference/exportrepositorywithmetadata.md),
  `ErrorResponse`), so `error` carries the message. The `/api/retention` pages document
  `error` and `message` both, and keep the row above.

It also found three deprecated legacy security endpoints that 7.x still serves:
`POST /api/security/users/authorization/expirePassword/{userName}`, `.../changePassword` and
`.../expirePasswordForAllUsers`. They are deprecated, the first two from 7.49.3, and
"archived here until being decommissioned"
([deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis.md)). artifactweave serves
them as [research 05](05-platform-identity-and-security-apis.md#legacy-artifactory-security-api-artifactoryapisecurity)
records.

## Supersession and integration boundaries

The [supersession register](../implementation/capability-roadmap.md#supersession-register)
names the replacement and migration obligation for each retired interface. Docker V1
transport and API-key issuance have named successors; SAML, SCIM, package formats and
private-index search are not superseded merely because another workflow exists.
[JFrog recommends AppTrust for RLM](https://docs.jfrog.com/releases/do/docs/release-lifecycle-management-deprecation-end-of-life),
but the requested Lifecycle/RBv2 compatibility and migration remain in P24/P25.
Pipelines EOL and Integration-service deprecation alone do not close their P40 work.

Mission Control, MyJFrog, JFConnect and bridges require explicit hosting, account,
entitlement and authorization contracts. P40 must distinguish operations on an
artifactweave estate from actions against an authorized JFrog estate and must not
report vendor-side success when nothing happened there. P26/P27 apply the same rule
to proprietary intelligence and scanning engines. Missing access or live captures
blocks the relevant acceptance obligation within its owning phase; it does not remove
the capability. Existing **[I]** observations remain on #216/P19, with expanded
qualification in P41.
