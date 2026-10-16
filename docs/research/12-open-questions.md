# 12: Open questions

Questions the research did not settle, who resolves each, and what each one blocks. Items
move out of this file into a decision record when they are resolved.

Research baseline: **2026-10-12**, revised **2026-10-13** for the enterprise products
([16-enterprise-products.md](16-enterprise-products.md)).

## Product decisions

| Question | Options | Resolver | Blocks |
|---|---|---|---|
| Product and binary name | **Resolved 2026-10-12: `artifactweave`** (matches hostweave, imageweave, guestweave, weaveoci) | project owner | nothing |
| OpenAPI 3.1 for our specs | Spike 2026-10-13: the bundled Artifactory spec converted to 3.1 (44 edits: `nullable` to type arrays, `allOf` wrappers unwrapped to `$ref` with sibling descriptions) generates and compiles with oapi-codegen v2.8.0, with identical models in the samples checked. Gains: JFrog's blocks are 3.1, so the scaffold stops down-converting; `$ref` siblings remove the 35 `allOf` wrappers; `const`, `examples` and type arrays become expressible. Costs: `TestOpenAPIFieldDescriptions`, the parity loader and the embedded-spec loader must handle `$ref` siblings; hostweave stays on 3.0.3; v2.8.0 calls its 3.1 support initial, so the whole three-spec set needs the same compile check plus a client round-trip. Options: switch now before P3 handlers exist; stay on 3.0.3 | owner | nothing; ADR 0002 records 3.0.3 until decided |
| Vault configurations | Specified in the Access spec with full descriptions; v1 answers 501, consistent with research 05 and 08. Implement when an external secret backend is in scope | owner | nothing |
| Protobuf for the internal model | Once the API schemas and [data model](15-data-model.md) are frozen, the internal entity model (ADR 0020) could be proto-first: scaffolded from the 53 entities, with buf lint and breaking checks as in the sibling repos, used for internal RPC (replication, later HA), events and SDKs, with `pkg/types` generated from it and a test that every OpenAPI wire field maps to one internal field. The REST surface stays OpenAPI-first because proto3 JSON cannot reproduce JFrog's wire quirks (string-typed booleans, kebab-case keys, epoch versus ISO dates, dynamic-key maps). Options: proto-first internal model now; plain Go `pkg/types` now and proto only when a gRPC or SDK consumer appears | owner, when a consumer is known | nothing in P1 or P2; `pkg/types` is plain Go until decided |
| Enterprise products scope | **Resolved 2026-10-13:** Distribution with release bundles v1, cold-storage archive and retention, platform federation and topology, federated repositories and Workers are in scope inside v0.1 (P13 to P17, milestone M4). Mission Control, MyJFrog, the SaaS bridges and JFConnect stay out | project owner | nothing |
| Peers: interoperate with JFrog? | **Resolved 2026-10-13:** artifactweave peers only; the management APIs stay JFrog-compatible and the traffic between instances is artifactweave's own protocol ([0021](decisions/0021-peer-trust-and-sync-protocol.md)) | project owner | nothing; recorded so the limitation is visible |
| Workers runtime | **Resolved 2026-10-13:** goja with esbuild in-process ([0026](decisions/0026-workers-runtime.md)) | project owner | nothing |
| Cold storage target | **Resolved 2026-10-13:** a cold blob tier inside the instance ([0024](decisions/0024-cold-storage-tier.md)); a separate cold instance over the peer protocol remains a possible later option | project owner | nothing |
| Workers memory cap | **Resolved 2026-10-13:** executions run one at a time in helper processes (the same binary, hidden `worker-host` subcommand), which makes a per-execution cap measurable with goja. Defaults follow JFrog's: 64 MiB memory, 1 s CPU, 5 s per call, 20 s overall, call depth 100, 100 KiB payload, 100,000-character source; interrupt on breach, kill and replace the helper at twice the cap ([0026](decisions/0026-workers-runtime.md)) | project owner | nothing |
| Peer protocol versioning | [0021](decisions/0021-peer-trust-and-sync-protocol.md) versions the protocol as `major.minor` with additive minors and refused majors. Open: how long a release supports the previous major (proposal: one release, so a rolling upgrade of a federation works), and whether the version is negotiated per stream or per peer | owner, before P13 closes | rolling upgrades of multi-instance deployments |
| Release bundles v2 distribution | the six v2 distribution operations are specified and answer 501 because they act on Lifecycle's release bundles v2, which stay out of scope ([0023](decisions/0023-release-bundles-and-distribution.md)). Revisit with Lifecycle | owner | nothing in v0.1 |
| High availability | single process in v1; later: PostgreSQL plus S3-only blobs, leader lease in the database, upload sessions in the store | owner, after M3 | nothing in v1; [ADR 0004](decisions/0004-storage-sql-metadata-and-blobs.md) states single-process |
| Web UI | none in v1; later a read-only browser over the storage API | owner | nothing; the JFrog CLI and Terraform are the clients |
| MySQL dialect | SQLite and PostgreSQL only in v1 | owner | nothing; `storetest` makes a third dialect a bounded task |
| Terraform backend repositories (`terraformbackend`) | implement the Terraform Enterprise API (`tfe.v2` discovery, organizations, workspaces, state versions, locking) that JFrog's `remote` backend form needs; or offer the simpler HTTP backend, which is incompatible with users' existing `backend "remote"` blocks | owner | cut from v1; the package type is rejected with 400 |
| LDAP timing | after M3 (P12) with bind and group sync through the Access v1 LDAP API and the YAML `security.ldapSettings` section | owner | `artifactory_ldap_setting_v2` and human login until then; v1 is local users only |
| Docker subdomain access method | repository-path routing is the native default; subdomain matching on `Host` is a configuration option; port per repository is out of scope | decided in [ADR 0003](decisions/0003-repository-model.md); revisit if users need port per repo | nothing |
| Reported version strings | 7.155.0 or newer on `/artifactory/api/system/version` and `/access/api/v1/system/version`, configurable | decided in [ADR 0001](decisions/0001-mirror-the-jfrog-api-surface.md); raise when a provider adds a higher gate | new provider features gated above the reported version |

## Captures still needed against a live Artifactory

These values are not documented precisely enough to hard-code. Each is captured from a
running Artifactory (a trial or an existing instance) before the corresponding handler is
frozen, and the captured request and response are checked in as fixtures.

| Item | What to capture | Blocks |
|---|---|---|
| Docker token challenge | **Resolved 2026-10-13** from public captures: realm `<base>/artifactory/api/docker/<repo|null>/v2/token`, `service` = addressed host with port, token by `GET` only, `{token, expires_in: 3600}` ([17](17-undocumented-shapes.md)) | nothing |
| npm web login | **Resolved 2026-10-13:** npm's own client contract (`loginUrl`, `doneUrl`, 202 with `Retry-After`, 200 `{token}`); Artifactory's URLs are opaque to npm ([17](17-undocumented-shapes.md)) | nothing |
| npm token endpoints | `GET /api/npm/auth` **resolved 2026-10-16:** the JFrog CLI calls it and writes the 200 body into `.npmrc`; decision [0034](decisions/0034-npm.md) fixes the body it answers ([25](25-npm.md) section 4). Still open: whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist on a current server, and the bodies of `api/npm/auth` and `/api/npm/<repo>/auth/<scope>` (#216) | P19 |
| PEP 658 metadata | **Resolved 2026-10-13:** no evidence Artifactory serves it; artifactweave implements it anyway, with HTTP Range ([17](17-undocumented-shapes.md)) | nothing |
| PyPI local storage layout | **Resolved 2026-10-13:** undocumented and inconsistent in public examples; artifactweave stores `<normalized-name>/<version>/<file>` and indexes by properties and file name, so clients are unaffected ([17](17-undocumented-shapes.md)) | nothing |
| AQL `$rf` | **Resolved 2026-10-13:** the CLI never sends it and no documentation mentions it; artifactweave answers 400 ([17](17-undocumented-shapes.md)) | nothing |
| Webhook signing | the exact header name and value when `use_secret_for_signing` is true (reported as `X-JFrog-Event-Auth` carrying an HMAC-SHA256 hex) | P11 |
| Xray stub reaction | how `jf audit` and `jf scan` report a 501 from `/xray`, to word the stub message | P18 |
| Federated repository plans | how `artifactory_federated_*_repository` reports a 400 when a `member` URL is not a paired peer (the only case still rejected now that federation is real), so the error message reads well in a plan | P13 |
| Release bundle v1 create body and states | the request and response of `POST /distribution/api/v1/release_bundle` (untyped in the oracle) and the wire names of the bundle states and per-target distribution statuses | P15 |
| Worker result statuses | the stop and warn statuses each before-action accepts beyond `DOWNLOAD_PROCEED`, the field name for worker secrets on `context`, and the execution-history record fields | P17 |
| Federation status payloads | the contents of `mirrorEventsStatusInfo` and `federatedArtifactStatus` in the repository status response (untyped in the oracle) | P13 |

## Items to re-verify when dependencies move

| Item | Why |
|---|---|
| Terraform provider version gates | the providers add gates with each release; the reported version must stay above the highest one |
| JFrog CLI status-code checks | `jfrog-client-go` hard-codes expected codes; a new release can change them |
| `oapi-codegen` OpenAPI 3.1 support | the specs stay on 3.0.3 until the strict server is verified on 3.1 |
| OCI distribution spec tag used by the conformance job | pinned per release |
