# 12: Open questions

Checked against: Artifactory 7.161.32

Questions the research did not settle, who resolves each, and what each one blocks.
Resolved entries retain links to their decisions so design closure is distinguishable
from implementation and from a still-needed live Artifactory capture.

Research baseline: **2026-10-12**, revised **2026-10-13** for the enterprise products
([16-enterprise-products.md](16-enterprise-products.md)).

Status reviewed after P8 on **2026-10-21**. The Terraform registry/backend, NuGet v2
subset and symbol reads, PyPI and Helm exit checks are implemented; the remaining
format-specific **[I]** observations stay on #216/P19. npm web approval (#262) and
package signatures (#362) remain separate follow-ups, despite the local package
phases being complete ([build status](../implementation/plan.md#build-status)).

## Product decisions

| Question | Options | Resolver | Blocks |
|---|---|---|---|
| Product and binary name | **Resolved 2026-10-12: `artifactweave`** (matches hostweave, imageweave, guestweave, weaveoci) | project owner | nothing |
| OpenAPI 3.1 for our specs | Spike 2026-10-13: the bundled Artifactory spec converted to 3.1 (44 edits: `nullable` to type arrays, `allOf` wrappers unwrapped to `$ref` with sibling descriptions) generates and compiles with oapi-codegen v2.8.0, with identical models in the samples checked. Gains: JFrog's blocks are 3.1, so the scaffold stops down-converting; `$ref` siblings remove the 35 `allOf` wrappers; `const`, `examples` and type arrays become expressible. Costs: `TestAccOpenAPIFieldDescriptions`, the parity loader and the embedded-spec loader must handle `$ref` siblings; hostweave stays on 3.0.3; v2.8.0 calls its 3.1 support initial, so the whole three-spec set needs the same compile check plus a client round-trip. The pre-P3 migration window has passed; current specs remain 3.0.3. A future migration must validate all seven service specs and their clients | owner | nothing; ADR 0002 records 3.0.3 until decided |
| Vault configurations | Access schema exists; current generated 501s. P38 implements external secret-backend integration, credential lifecycle and effective key resolution | P38 | Vault-backed configuration and `vaultKey` workflows |
| Protobuf for the internal model | Once the API schemas and [data model](15-data-model.md) are frozen, the internal entity model (ADR 0020) could be proto-first: scaffolded from the 53 entities, with buf lint and breaking checks as in the sibling repos, used for internal RPC (replication, later HA), events and SDKs, with `pkg/types` generated from it and a test that every OpenAPI wire field maps to one internal field. The REST surface stays OpenAPI-first because proto3 JSON cannot reproduce JFrog's wire quirks (string-typed booleans, kebab-case keys, epoch versus ISO dates, dynamic-key maps). Current implementation: plain Go `pkg/types`; reconsider protobuf only when a gRPC or SDK consumer appears | owner, when a consumer is known | nothing for the implemented stores; `pkg/types` remains plain Go |
| Enterprise products scope | The 2026-10-13 expansion added P13–P17. The 2026-10-21 owner direction in [0042](decisions/0042-capability-scope-and-supersession.md) brings every remaining capability into the [P20–P41 roadmap](../implementation/capability-roadmap.md), except specifically evidenced superseded interfaces | resolved scope; owning phases implement | no capability may remain an unowned exclusion |
| Peers: interoperate with JFrog? | The initial P13–P15 design uses artifactweave peers and its own transport. P37 owns vendor federation/distribution and separate cold-instance contract research and interoperability; P40 owns estate/bridge integration. Undocumented protocols and unavailable vendor accounts are tracked dependencies | P37/P40 | cross-vendor qualification |
| Workers runtime | **Resolved 2026-10-13:** goja with esbuild in-process ([0026](decisions/0026-workers-runtime.md)) | project owner | nothing |
| Cold storage target | P16 first implements an internal cold blob tier ([0024](decisions/0024-cold-storage-tier.md)); P37 owns separate cold-instance pairing, transfer and restore contracts | P16/P37 | archive execution first, external/cold-instance workflows later |
| Workers memory cap | **Resolved 2026-10-13:** executions run one at a time in helper processes (the same binary, hidden `worker-host` subcommand), which makes a per-execution cap measurable with goja. Defaults follow JFrog's: 64 MiB memory, 1 s CPU, 5 s per call, 20 s overall, call depth 100, 100 KiB payload, 100,000-character source; interrupt on breach, kill and replace the helper at twice the cap ([0026](decisions/0026-workers-runtime.md)) | project owner | nothing |
| Peer protocol versioning | [0021](decisions/0021-peer-trust-and-sync-protocol.md) versions the protocol as `major.minor` with additive minors and refused majors. Open: how long a release supports the previous major (proposal: one release, so a rolling upgrade of a federation works), and whether the version is negotiated per stream or per peer | owner, before P13 closes | rolling upgrades of multi-instance deployments |
| Release bundles v2 distribution | Distribution v2 operations currently return generated 501s; Lifecycle is a prefix 501 and Access stages/lifecycle routes are absent. P24 implements the bundle/promotion/distribution/cleanup contracts with P23 evidence and shared P25 AppTrust foundations | P24/P25 | native release-bundle and Terraform workflows |
| High availability | Current contract is single-process. P35 implements shared sessions/staging, distributed leases and scheduling, failover and rolling upgrades; the exact supported storage/database combinations require proof | P35 | multi-node runtime and recovery |
| Web UI | No general console today. P21 owns npm/Terraform browser approval (#262) and human authentication; P39 owns artifact, administration, governance and operations workflows | P21/P39 | browser workflows and complete console |
| MySQL dialect | SQLite and PostgreSQL work; P34 adds MySQL and database-portability contract proof | P34 | MySQL deployments |
| Terraform backend repositories (`terraformbackend`) | **Resolved 2026-10-19:** serve the Terraform Enterprise API that Terraform's `remote` backend speaks (discovery, organisations, workspaces, locks, state versions) under `/api/terraform/remote/v2/`, as 7.x does; the simpler HTTP backend would not work with users' existing `backend "remote"` blocks ([0041](decisions/0041-terraform-backend.md), [research 32](32-terraform-backend.md)) | owner | nothing; P8 serves it (#557, #558) |
| LDAP timing | after M3 (P12) with bind and group sync through the Access v1 LDAP API and the YAML `security.ldapSettings` section | owner | `artifactory_ldap_setting_v2` and LDAP-backed login until then; local-user authentication and OIDC workload token exchange already work |
| Docker subdomain access method | Path and configured subdomain routing work. P28 owns port-per-repository routing and client compatibility; it is not a permanent cut | P28 | port-based registry deployments |
| Reported version strings | 7.155.0 or newer on `/artifactory/api/system/version` and `/access/api/v1/system/version`, configurable | decided in [ADR 0001](decisions/0001-mirror-the-jfrog-api-surface.md); raise when a provider adds a higher gate | new provider features gated above the reported version |

## Captures still needed against a live Artifactory

The scope decision is settled; contract uncertainties remain. P23–P27 own Evidence,
Lifecycle/AppTrust, Catalog/OneModel and security/PTC captures. P40 owns account,
entitlement, Mission Control, bridge and legacy integration contracts. A vendor-only
service needs an authorized integration or a clearly identified analogue, never a
fabricated successful vendor operation. Existing #216/P19 items below remain open
until captured; P41 qualifies the expanded surface.

These values are not documented precisely enough to hard-code. Each is captured from a
running Artifactory (a trial or an existing instance) before the corresponding handler is
frozen, and the captured request and response are checked in as fixtures.

| Item | What to capture | Blocks |
|---|---|---|
| Docker token challenge | **Resolved 2026-10-13** from public captures: realm `<base>/artifactory/api/docker/<repo|null>/v2/token`, `service` = addressed host with port, token by `GET` only, `{token, expires_in: 3600}` ([17](17-undocumented-shapes.md)) | nothing |
| npm web login | **Resolved 2026-10-13:** npm's own client contract (`loginUrl`, `doneUrl`, 202 with `Retry-After`, 200 `{token}`); Artifactory's URLs are opaque to npm ([17](17-undocumented-shapes.md)) | nothing |
| npm token endpoints | `GET /api/npm/auth` **resolved 2026-10-16:** the JFrog CLI calls it and writes the 200 body into `.npmrc`; decision [0034](decisions/0034-npm.md) fixes the body it answers ([25](25-npm.md) section 4). Still open: whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist on a current server, and the bodies of `api/npm/auth` and `/api/npm/<repo>/auth/<scope>` (#216) | P19 |
| PEP 658 metadata | **Resolved 2026-10-19:** 7.x serves it behind a system property, off unless set ([J] RTFACT-31470); artifactweave serves it behind `pypi.core_metadata`, off by default, and HTTP Range always ([28](28-pypi.md#4-core-metadata-files-pep-658-and-pep-714), decision [0037](decisions/0037-pypi.md)). The property's name and the attributes 7.x writes stay on #216 | P19 |
| PyPI local storage layout | **Resolved 2026-10-19:** 7.x stores twine uploads at `<name>/<version>/<file>`, the folder with `-` for `_`, and indexes files by their metadata wherever they are ([J] RTFACT-20390, RTFACT-29973, RTFACT-29892); artifactweave stores `<normalized-name>/<version>/<file>` and indexes the same way, so clients are unaffected ([28](28-pypi.md#6-storage-properties-and-indexing), decision [0037](decisions/0037-pypi.md)). 7.x's folder for names with upper case or dots stays on #216 | P19 |
| AQL `$rf` | **Resolved 2026-10-13:** the CLI never sends it and no documentation mentions it; artifactweave answers 400 ([17](17-undocumented-shapes.md)) | nothing |
| Webhook signing | the exact header name and value when `use_secret_for_signing` is true (reported as `X-JFrog-Event-Auth` carrying an HMAC-SHA256 hex) | P11 |
| Xray stub reaction | Existing prefix 501 failures remain honest until P26/P27 implement scanning/Curation. Capture real CLI behavior and the security service contracts as part of those phases; matching the failure envelope alone is not the exit | P26/P27 and P41 | security client qualification |
| Federated repository plans | how `artifactory_federated_*_repository` reports a 400 when a `member` URL is not a paired peer under P13's planned peer implementation, so the error message reads well in a plan; multi-member synchronisation is not implemented yet | P13 |
| Release bundle v1 create body and states | the request and response of `POST /distribution/api/v1/release_bundle` (untyped in the oracle) and the wire names of the bundle states and per-target distribution statuses | P15 |
| Worker result statuses | the stop and warn statuses each before-action accepts beyond `DOWNLOAD_PROCEED`, the field name for worker secrets on `context`, and the execution-history record fields | P17 |
| Federation status payloads | the contents of `mirrorEventsStatusInfo` and `federatedArtifactStatus` in the repository status response (untyped in the oracle). In 7.x `status/repo` is legacy-federation only, which RTFS does not serve ([16](16-enterprise-products.md#4-federated-repositories)), so the capture needs a trial on legacy federation | P13 |
| Release bundle view paths | the `GET .../{name}/{version}/status?type=target`, `GET .../{name}/{version}/artifacts` and target-side `DELETE /api/release/bundles/{name}/{version}` that the 7.146.7 release notes list and the API reference does not ([16](16-enterprise-products.md#1-distribution-and-release-bundles-v1)) | P15 |
| Worker actions without a catalogue entry | whether 7.x still accepts `AFTER_SAVE`, `BEFORE_UPLOAD_REQUEST`, `BEFORE_REMOTE_INFO_REQUEST` and `AFTER_WORKLOAD_STATE_CHANGE_REQUEST` ([16](16-enterprise-products.md#the-action-catalogue)) | P17 |
| Platform Federation global entities | how JFrog's Grid product carries projects, stages, users, groups, permission targets and tokens between SaaS sites, which its documented pfed API does not show ([0025](decisions/0025-platform-federation-and-topology.md)) | P14 |

## Items to re-verify when dependencies move

| Item | Why |
|---|---|
| Terraform provider version gates | the providers add gates with each release; the reported version must stay above the highest one |
| JFrog CLI status-code checks | `jfrog-client-go` hard-codes expected codes; a new release can change them |
| `oapi-codegen` OpenAPI 3.1 support | the specs stay on 3.0.3 until the strict server is verified on 3.1 |
| OCI distribution spec tag used by the conformance job | pinned per release |
