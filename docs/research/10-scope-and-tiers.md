# 10: Scope and tiers

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

Which JFrog product APIs artifactweave implements, shims, stubs or ignores, which error
envelope each surface uses, and what the known clients do when they hit a stub. The
tiering is the contract behind [ADR 0001](decisions/0001-mirror-the-jfrog-api-surface.md);
the per-endpoint detail lives in documents 02 through 09.

Research baseline: **2026-10-12**, revised **2026-10-13** when the project owner brought
Distribution with release bundles v1, cold-storage archive and retention, platform
federation and topology, federated repositories and Workers into scope
([16-enterprise-products.md](16-enterprise-products.md)). Client reactions come from the JFrog CLI and Terraform
provider sources cited in [08-terraform-providers.md](08-terraform-providers.md) and
[09-jfrog-cli-compatibility.md](09-jfrog-cli-compatibility.md). Reactions marked
*(unverified)* have not been observed against a running system.

## Tiers

| Tier | Meaning |
|---|---|
| implement | full behaviour, byte-compatible on the wire, covered by acceptance tests with the real clients |
| shim | a thin adapter over the same internal model as the modern API, kept because a client still calls it |
| stub | the path is routed and answers 501 in the product's own error envelope, so clients fail predictably |
| ignore | no route; the router's 404 in the Artifactory envelope |

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
| Terraform registry (`/api/terraform/v1/...`) | `{"errors":["<string>"]}` |
| Go proxy, PyPI simple, Helm | plain text |
| NuGet | text or XML, matching what `NuGet.Protocol` tolerates |

The table is implemented once in `internal/server/apierror` and tested per surface.

## Product families

| Product and paths | What it does | Tier | Client reaction |
|---|---|---|---|
| Artifactory core (`/artifactory/api/repositories`, `/api/storage`, `/api/search`, `/api/copy`, `/api/move`, `/api/trash`, `/api/system/storage/gc`, `/api/storageinfo`, `/{repo}/{path}`) | repositories, artifacts, properties, search | implement | |
| Package protocols (`/v2`, `/api/npm`, `/api/go`, `/api/pypi`, `/api/nuget`, `/api/helm`, `/api/terraform`, Maven plain paths) | the eight ecosystems | implement | |
| Package-type APIs (reindex, calculateMetadata, docker promote and delete) for the eight ecosystems | metadata maintenance | implement | |
| Docker V1 registry API (`dockerApiVersion: V1`, the `/v1/` registry paths) | the legacy Docker registry protocol | ignore: artifactweave serves the Docker registry API V2 and the OCI distribution specification only. `dockerApiVersion: V1` is accepted and stored so repository configuration round-trips (`pkg/repo/repo.go`), and `/artifactory/api/docker/<repo>/v1/auth` answers the `config.json` entry (`internal/server/docker/docker.go`). Self-managed 7.x still documents V1 repositories, which are "not supported on Cloud instances", and neither its deprecations nor its end-of-life page removes them (https://docs.jfrog.com/artifactory/docs/additional-docker-information#use-docker-v1, https://docs.jfrog.com/releases/docs/artifactory-deprecations, https://docs.jfrog.com/releases/docs/artifactory-end-of-life; DOCKER-OCI-11 in [#378](https://github.com/weaveplatform/artifactweave/issues/378)) | a V1 registry request answers 404. Docker itself removed interacting with V1 registries in 17.12 (`docs/deprecated.md` in `docker/cli`) |
| Other ecosystems' package-type APIs (alpine, cargo, conan, conda, cran, debian, gems, opkg, pub, puppet, rpm, swift, vcs) | | ignore | `jf rt` commands for those types fail with 404 |
| Replication (`/api/replications`, `/api/replication`) | push and pull | implement single push and pull; multi-push and event-based replication stub | `artifactory_local_repository_multi_replication` fails at apply with 501 |
| Legacy security (`/api/security/users`, `groups`, `permissions`, `apiKey`, `token`) | pre-Access identity | shim | `jf rt user-*`, `artifactory_group`, `artifactory_permission_target` keep working; API-key creation answers 400 |
| Access identity (`/access/api/v2/users`, `groups`, `permissions`; `/access/api/v1/tokens`, `roles`, `environments`, `oidc`) | identity, tokens, OIDC exchange | implement | |
| Projects (`/access/api/v1/projects`) | projects, roles, membership, repo attach and share | implement | |
| Webhooks (`/event/api/v1/subscriptions`) | event subscriptions | implement (events emitted for `artifact`, `artifact_property`, `docker`, `build`, `user`; other domains accepted but never fire) | all 24 `artifactory_*_webhook` resources apply cleanly |
| Package cleanup policies (`/artifactory/api/cleanup/packages/policies`) | retention | implement | |
| Build cleanup and release bundle cleanup policies (`/api/cleanup/builds`, `/api/cleanup/bundles`) | | stub | `artifactory_release_bundle_v2_cleanup_policy` fails at apply with 501 |
| Build-info (`/api/build`) | build publishing and promotion | implement, except the Builds Diff (`GET /api/build/{name}/{number}?diff=`), which answers 501 ([DV-088](../implementation/deviations.md#api-behaviour)) | the JFrog CLI never asks for a diff |
| AQL `item.infos` domain (`item.infos.find(...)`, documented in [aql-entities-fields-reference](https://docs.jfrog.com/artifactory/docs/aql-entities-fields-reference.md)) | the time, author and checksum of an item's last property change | not served: answers 400 naming the domain as unsupported, as AQL does for valid queries it does not serve ([0012](decisions/0012-search-and-aql.md#decision)). The item store keeps the time of the last property change but not who made it or a checksum of the properties | the JFrog CLI never queries it |
| System (`/api/system/ping`, `version`, `license`, `configuration` YAML PATCH and XML GET, `webServer`, `storageinfo`, readiness, liveness, router health, usage, jfconnect metrics) | | implement | |
| Debug loggers and locks | | implement (`/api/system/debug/loggers`, `/api/system/locks`) | Loggers change slog levels in memory for a bounded time; locks report artifactweave's lease table (research 15) |
| System configuration XML `POST`, import and export, SHA-256 migration | | stub (`/api/import`, `/api/export`, `/api/artifactexport`) or ignore | `jf rt logs` is not affected; no Terraform resource uses them |
| External authentication: LDAP (`/access/api/v1/ldap`, YAML `security.ldapSettings`) | directory login | implement in a later phase | `artifactory_ldap_setting_v2` fails at apply with 501 until then |
| External authentication: SAML, SCIM, Crowd, HTTP-SSO (`/access/api/v1/saml`, `scim`, `crowd`, `httpsso`) | | stub | `platform_saml_settings`, `platform_scim_*`, `platform_crowd_settings`, `platform_http_sso_settings` fail at apply with 501 |
| Xray and JFrog Security (`/xray/api/v1|v2/**`, Curation `/xray/api/v1/curation/**`) | scanning, watches, policies, violations, SBOM, curation | stub, in Xray's envelope; `xrayIndex` on repositories is persisted and echoed | `jf rt` commands never touch `/xray` and the CLI does not probe it (`jfrog-cli-core/utils/xray/xrayutils.go` has no version probe or 404 handling); `jf audit`, `jf scan` and `jf curation-audit` fail on the 501. No Terraform provider in scope calls Xray at configure time |
| Distribution and release bundles v1 (`/distribution/**`, `releasebundles` package type, `/artifactory/api/release/**`, `/artifactory/api/v1/bundle/retention/**`) | signed bundles pushed to edges | implement, with artifactweave instances as edges ([0023](decisions/0023-release-bundles-and-distribution.md)); `trigger_xray_scan` is a stub because Xray is out of scope | `jf ds` commands and `artifactory_distribution_public_key` work; the distribution webhook domains fire |
| Lifecycle, AppTrust, Evidence (`/lifecycle/**`, `/apptrust/**`, `/evidence/**`, `/access/api/v2/stages`, `/access/api/v2/lifecycle`) | release bundles v2, promotion, applications, attestations | stub, except environments (`/access/api/v1/environments`), which are implemented because repository configuration references them | `artifactory_release_bundle_v2`, `artifactory_release_bundle_v2_promotion`, `platform_lifecycle_stage`, `platform_lifecycle` fail at apply with 501; `jf release-bundle-*` fails |
| Workers (`/worker/**`) | TypeScript hooks on platform events | implement on goja with esbuild ([0026](decisions/0026-workers-runtime.md)) | `platform_workers_service` applies |
| Federated repositories (`rclass: federated`, `/api/federation/**`, `/api/v1/service_trust/pairing`) | multi-writer repository sync | implement between artifactweave peers ([0021](decisions/0021-peer-trust-and-sync-protocol.md), [0022](decisions/0022-federated-repositories.md)) | `artifactory_federated_*_repository` applies with members that are paired peers; an unpaired member URL is rejected with 400 naming it |
| Cold storage and archive (`/api/retention/archive/**`, `/api/archive/v2/**`, `/api/retention/search`, `searchPackages`, `restore`, `namespace`) | moving rarely used content to cheaper storage | implement as a cold blob tier inside the instance ([0024](decisions/0024-cold-storage-tier.md)) | `artifactory_archive_policy` applies |
| Pipelines (`/pipelines/**`) | sunset CI/CD | ignore | |
| Live Logs (`/api/system/logs/*`) | stream service logs | stub (501), specified since P1; cheap to implement later | `jf rt logs` fails with 501 |
| Access Federation (`/access/api/v1/system/federation`), Platform Federation (`/pfed/api/v1/**`), Grid topology and monitoring (`/grid/api/v1/**`) | identity sync and topology across instances | implement with every JPD an artifactweave peer ([0025](decisions/0025-platform-federation-and-topology.md)) | no Terraform resource in the providers in scope |
| Mission Control, JPDs, licence buckets (`/mc/api/v1/**`) | licence and deployment management for JFrog estates | ignore (404): licences do not exist in artifactweave and peers are managed through pairing and `/pfed` | |
| SaaS bridges (`/bridge-client/**`, `/bridge-server/**`) | connecting JFrog SaaS to self-hosted deployments | ignore: there is no SaaS side to bridge to | |
| JFConnect (`/jfconnect/api/v1/**`) | licence entitlement and usage reporting to JFrog | ignore, except the metrics sink every `jf` command posts to, which answers 2xx | |
| MyJFrog (`my.jfrog.com`, `/api/jmis/**`, `/api/usage/**`, `/api/v1/offline_register`) | JFrog's SaaS account portal | ignore: a different host and an account artifactweave does not have | |
| AWS IAM role mapping, Vault configuration, log shipping | cloud integrations | stub (501) | `platform_aws_iam_role`, `artifactory_vault_configuration` fail at apply with 501 |
| Signed URLs (`/api/signed/url`, `/api/signed/url/key`), `/api/v1/metrics/application` | Enterprise and Cloud extras | stub | |
| Direct cloud storage download and CDN redirect (`downloadRedirect`, `cdnRedirect`, `X-JFrog-Download-Redirect-To`) | redirecting downloads to the object store or a CDN | implement ([0029](decisions/0029-cdn-delivery-and-download-redirect.md)) | `jf rt dl` follows the redirect, including split downloads |

## Artifactory 7.x check

Checked on 2026-10-17 against the self-managed release notes up to 7.161.32
(https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases), the legacy notes
for 7.0 to 7.83 (https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf) and the deprecations page
(https://docs.jfrog.com/releases/docs/artifactory-deprecations). The tier table above
stands. It was corrected in one place: CDN redirect was listed as a stub, but it has been
implemented since P4b. What 7.x changed in the products the table lists:

| Product | 7.x | Source |
|---|---|---|
| Federated repositories | Served by the standalone Artifactory Federation Service (RTFS) at `/rtfs` from 7.111.12. Four status operations are legacy-only, and new features such as unidirectional sync are RTFS-only ([research 16](16-enterprise-products.md#4-federated-repositories)) | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed |
| Cold storage and archive | Smart archiving also serves a self-hosted active instance (hybrid, Enterprise+, from 7.117.14) | https://docs.jfrog.com/administration/docs/archive.md |
| Platform Federation and Grid | The Grid product syncs projects and identity on SaaS only (7.154.0); the documented `/pfed` API still carries Curation entity types | https://docs.jfrog.com/administration/docs/jfrog-grid.md |
| Workers | Scheduled workers from 7.104.5; repository events on self-managed from 7.161.15 | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed, https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed |
| Distribution and release bundles v1 | Still available; the v1 Replicator was deprecated in 7.84.10 | https://docs.jfrog.com/releases/docs/artifactory-deprecations |
| Pipelines | Confirmed sunset: end of life on 1 June 2025 | https://releases.jfrog.io/artifactory/legacydocs/JFrog-Pipelines-End-of-Life-Jun-1-2025.pdf |
| Mission Control | Its user plugins were removed in 7.117; Access Federation still needs Mission Control and a Circle of Trust | https://docs.jfrog.com/releases/docs/artifactory-deprecations, https://docs.jfrog.com/administration/docs/access-federation.md |

**New 7.x services the table does not list.** The table has no row for:
- the Topology service and One Model, a GraphQL hub (7.104.5);
- the Integration service, deprecated in 7.77.3;
- Package Traffic Controller (7.146.35).

Whether any client in scope calls these services is **[I]**, so they get no tier until a
client needs one.

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


Recorded with their reasons in [12-open-questions.md](12-open-questions.md):
`terraformbackend` (needs the Terraform Enterprise API), NuGet v2 OData beyond what
`NuGet.Protocol` sends for restore, PyPI XML-RPC search, the Go sumdb proxy (clients fall
back to `sum.golang.org` on 404), npm audit proxying, XML descriptor `POST`, import and
export, multi-push and event-based replication, SAML, SCIM, Crowd and HTTP-SSO, API-key
creation, Go VCS-backed remotes, a web UI, MySQL, high availability. Federation across
instances and cold storage were cut until 2026-10-13 and are now in scope; interoperating
with Artifactory or Edge peers remains out.

## What the rules buy

The JFrog CLI degrades on its own: it never probes Xray, it tolerates a `supported: false`
multipart answer, and its telemetry posts are fire-and-forget. The Terraform providers do
not probe optional products at configure time; a resource for a stubbed product fails only
when it is applied, with the 501 message naming artifactweave and the product. Plans that
contain only in-scope resources therefore apply cleanly, and a plan that includes an
out-of-scope resource fails on that resource alone.

Primary URLs: https://docs.jfrog.com/integrations/docs/openapi-specifications,
https://docs.jfrog.com/security/reference/xray-rest-apis/llms.txt,
https://docs.jfrog.com/security/reference/jfrog-curation-api/llms.txt,
https://docs.jfrog.com/artifactory/reference/jfrog-distribution-api/llms.txt,
https://docs.jfrog.com/artifactory/reference/release-bundles-v1-api/llms.txt,
https://docs.jfrog.com/governance/reference/llms.txt,
https://docs.jfrog.com/administration/reference/createworker.md,
https://docs.jfrog.com/integrations/reference/jfrog-artifactory-live-logs-api/llms.txt,
https://docs.jfrog.com/artifactory/docs/access-federation-vs-repo-federation.md,
https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt,
https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt,
https://github.com/jfrog/jfrog-cli-core/blob/master/utils/xray/xrayutils.go,
https://docs.jfrog.com/administration/docs/cold-storage.md,
https://docs.jfrog.com/administration/docs/access-federation.md,
https://docs.jfrog.com/administration/docs/jfrog-grid.md,
https://docs.jfrog.com/administration/docs/typescript-code-for-workers.md.
