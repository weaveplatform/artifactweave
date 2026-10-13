# 13: Review log

Before the plan was approved, three independent reviewers challenged it: an architecture
critic, a fact-checker for the Artifactory core and protocol inventories (documents 02 to
04), and a fact-checker for the platform, Terraform and CLI inventories (documents 05 to
09). This log records what they found and what changed, so that a reader can tell which
parts of the design are the result of a correction rather than a first guess.

Research baseline: **2026-10-12**.

## Method

- The core fact-checker read all 160 reference pages in the seven
  `docs.jfrog.com/artifactory/reference/*` indexes, parsed every embedded OpenAPI block,
  and compared paths, parameters, defaults, enums and responses against the draft. It also
  read the JFrog Terraform provider and CLI sources and the upstream protocol
  specifications.
- The platform fact-checker read the provider sources at the versions in
  [08-terraform-providers.md](08-terraform-providers.md), `jfrog-client-go` v1.55.0,
  `jfrog-cli-core` v2.60.0 and the docs.jfrog.com pages for every endpoint named.
- The architecture critic read the plan, the repository template conventions, and the
  sibling repositories whose conventions the plan reuses.

## Architecture findings and the resulting changes

| # | Finding | Change |
|---|---|---|
| 1 | The second phase's CLI exit criteria depended on AQL, which was scheduled later | the CLI's AQL query shape became a milestone inside the repositories phase, captured as fixtures; the full grammar follows later |
| 2 | Multi-segment path parameters and mode-switch responses cannot be expressed by the strict OpenAPI server | the strict server is scoped to flat CRUD families; item-path endpoints are hand-written handlers kept in the spec as `x-handwritten` |
| 3 | No Docker daemon on the macOS and Windows CI runners | acceptance and container-backed suites run on Linux only; elsewhere they skip |
| 4 | No package or version model for cleanup, tag retention, npm search, NuGet registration | a package index with per-type version comparators ([ADR 0018](decisions/0018-package-index.md)) |
| 5 | Secrets at rest were undefined | sealed columns and a master key ([ADR 0017](decisions/0017-sealed-columns-and-master-key.md)) |
| 6 | The Docker subdomain and port methods contradicted the single-listener design, and the repository-path method was missing | repository-path routing is the native default ([ADR 0003](decisions/0003-repository-model.md)) |
| 7 | Base URL precedence and `X-JFrog-Override-Base-Url` were missing | one base-URL resolver with Artifactory's precedence |
| 8 | Storage API and AQL path representations were unreconciled | a canonical item model in [ADR 0004](decisions/0004-storage-sql-metadata-and-blobs.md) |
| 9 | `<repo>-cache` as a real repository would leak into listings | a repository-key resolver with a `cached` flag ([ADR 0007](decisions/0007-remote-repositories.md)) |
| 10 | The Terraform backend protocol was undecided | `terraformbackend` cut from v1 |
| 11 | High availability was asserted without any HA-capable component | the storage record states single-process; HA is a later phase |
| 12 | Large uploads (S3 multipart, Docker chunks, CLI multipart) were undesigned | a blob upload-session contract; the CLI's multipart probe answers `supported: false` |
| 13 | Range requests and download redirects were absent | ranged reads and presigned URLs in the blob contract |
| 14 | Checksum sidecars and `originalChecksums` were undesigned | sidecars served from metadata and validated per `checksumPolicyType` |
| 15 | AQL-to-SQL dialect traps were unnamed | named in [ADR 0012](decisions/0012-search-and-aql.md); fixtures run on both dialects |
| 16 | `tagliatelle` per-package rules cannot express kebab and camel case inside one struct | wire models live in ignored packages; the exact lint configuration is in [ADR 0002](decisions/0002-spec-first-flat-crud-handwritten-item-paths.md) |
| 17 | `terraform init` proves nothing | the exit criterion is `terraform plan` with a data source; CI uses a provider mirror |
| 18 | The licence check was described three different ways | the provider source was read: there is no licence check |
| 19 | Xray's envelope is `{"error"}` and client reactions to 501 were unresearched | an error table per surface; reactions recorded in [10-scope-and-tiers.md](10-scope-and-tiers.md) |
| 20 | API keys were both implemented and declared end of life | shims only |
| 21 | LDAP, SAML, Crowd, SSO and SCIM were untiered | [ADR 0019](decisions/0019-human-authentication-tiers.md) |
| 22 | Built-in principals and default permission targets were not a deliverable | seeded at first start |
| 23 | Publishing needs credentials, so format phases cannot run before identity | the format phases depend on the identity phase |
| 24 | Go remotes resolve only through a virtual | the remote phase's exit excludes Go; Go is proven in the virtual phase |
| 25 | Build-info was scheduled too late and the decision text contained sequencing | build-info moved earlier; sequencing removed from decision records |
| 26 | The acceptance suite was over-containerised and unbudgeted | tool binaries for the Go-based clients, cached volumes, a storage-tagged subset, a 15-minute budget |
| 27 | The OCI conformance mechanics were hand-waved | a separate job with a pinned spec tag and `OCI_NAMESPACE` |
| 28 | The 90 percent per-package floor needs fault injection for streaming handlers | shared failing fakes and fault modes in the fake upstreams |
| 29 | Protocol error envelopes were missing | the errors table covers npm, Go, PyPI, NuGet, Helm, Terraform and Xray |
| 30 | The Quartz cron parser was unnamed | candidates named, chosen by a fixture of documented cron strings |
| 31 | Remote retrieval semantics were too flat | a decision table per item kind and setting in [ADR 0007](decisions/0007-remote-repositories.md) |
| 32 | Matrix-parameter resolution, version tokens, archive explode and archive browsing were unscheduled | added to the repositories and format phases |
| 33 | No threat model, operator guide, configuration reference, CI document or decision template | added to the document set |
| 34 | Research and decision records would duplicate each other | research holds facts and citations; decision records hold decisions and link |
| 35 | Package naming inconsistencies | `internal/format/golang`; one generated client per spec |
| 36 | No early milestone proved the thesis | milestones M1 to M3 and an explicit v1 cut list |

## Core API and protocol corrections

Wrong in the draft and corrected in documents 02 to 04:

- A checksum mismatch on deploy is 409, not 400; a virtual target answers 405.
- Terraform registry paths are host-rooted with the repository key encoded in the
  namespace (`<repo>__<ns>` for modules, `<repo>-rt-ns-<ns>` for providers); the per-repo
  path exists only for the network mirror.
- `_catalog` is a Docker Registry V2 extension, not part of the OCI distribution spec.
- Helm's "six minutes" is a merge-lock timeout; the virtual cache TTL is
  `virtualRetrievalCachePeriodSecs`.
- The Helm OCI default since 7.75.3 is a web UI toggle only; the REST `helm` package type
  still creates a classic repository.
- PEP 691 JSON must report api-version 1.1 with both `dist-info-metadata` and
  `core-metadata`.
- Open Metrics licensing was misattributed.
- npm "enforce strict layout" moves packages rather than rejecting them.

Gaps filled: repository create and update return 200 with a text body; repository delete
status codes; the existence response shape; `RELEASE_BUNDLE` in the configurations
listing; `?lastModified` needs deploy permission; properties `recursive` defaults to 1;
PATCH property values may be string, array or null; the folder-download setting; trash
status codes and permissions; the latest-by-properties response shape; AQL `distinct`
default and the admin-only `builds` domain; `$rf` unverified; `terraformType` case
handling; `encryptStates` default true; string-typed booleans per repository class;
federated `members.enabled` is a string; remote `force*Authentication` fields; JSON keys
known only from the provider; package types allowed per class; OCI end-4c, end-7b and
`OCI-Subject`; the Docker challenge `service` value unverified; the GOPROXY `|` separator;
the npm web-login `done` endpoint; npm token endpoints and `api/npm/auth` unverified; npm
audit on virtuals only; PEP 658 and the PyPI local layout unverified; Terraform provider
files indexed by filename at any depth; the `remote` backend needs `tfe.v2`.

## Platform, Terraform and CLI corrections

Wrong in the draft and corrected in documents 05 to 09:

- No Terraform provider checks the licence; configure time is a version read and a usage
  ping, and the platform provider also reads the Access version. The reported version is
  7.155.0 or newer on both endpoints and no licence string is needed.
- There are no untyped `artifactory_local`, `artifactory_remote` or `artifactory_virtual`
  resources.
- Repository delete is a bare `DELETE` that must return exactly 200.
- `artifactory_item_properties` updates through `PATCH /api/metadata`.
- Webhook event names for release bundles v2 are `release_bundle_v2_started`,
  `_failed`, `_completed`, and the promotion names follow the same pattern.
- The password expiration policy must be real boolean and integer JSON.
- On `/api/v2/security/permissions/{name}`, POST creates only (201), PUT creates or
  replaces (200), HEAD checks existence, DELETE returns 200.
- The CLI has no startup version check and never probes Xray.
- The CLI's sha256 header is `X-Checksum`; checksum deploy falls through on any
  non-2xx status; the AQL suffix order is include, sort, offset, transitive, limit with
  `"path":{"$ne":"."}` clauses.
- `platform_group` has no 7.49.3 gate.

Gaps filled, now the status-code contract: API-key POST 201 and the `AKCp8` recognition
rule; the `unlockUsers/{name}` path form; legacy group PUT 201; scoped token POST 200 with
`token_id` and an empty `project_key`; token defaults (3600, 0 means never, 21600 s
revocable threshold, 24 h refresh grace); reference tokens of exactly 64 characters; the
JWT payload must decode with `RawStdEncoding`; the multipart probe must answer 200; set and
delete properties 204; copy and move 200; folder PUT 201; `PUT api/build` 200, 201 or 204;
build retention 204; the promote content type; repository CRUD 200; the `jfrog-cli-go`
User-Agent; the jfconnect metrics stub; extra webhook criteria; the signing header modes;
`webServer` `httpsPort` and `DIRECT`; OIDC create 200 with `organization` and the
`provider_type` enum; project `soft_limit`, `storage_quota_email_notification`, attach 204
followed by a synchronous `projectKey`; global role POST 201 with `{code,message}`; the
project-role validator lacks `READ_POLICIES_SECURITY`; cleanup run statuses include the
warm states, `?checkRepoAvailability`, delete 200; `TokenDetails` has no `audience`; the
per-repository public key endpoint; distribution key POST 201 and its listing shape;
global environment 201, 200 and 204; password policy 204; legacy user `status` is upper
case where Access v2 is lower case; the legacy token create endpoint is still used by the
CLI; no SSH-key endpoints exist.

## What survived unchanged

Citing rather than vendoring the proprietary specifications; sha256 content addressing
with sha1 and md5 indexed; a native permission evaluator rather than a policy language; the
JWT subject and scope design; events emitted from services rather than handlers; treating
`federated` as local when the only member is the instance itself; the three configuration
layers; chi fakes for upstreams; generated code and entry points excluded from coverage;
Prometheus text at the metrics path regardless of licence tier.
