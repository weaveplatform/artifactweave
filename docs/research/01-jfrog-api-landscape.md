# JFrog API landscape

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)). **[J]** marks
a JFrog Jira issue, as in the [research README](README.md#target-version-and-sources).

Research baseline: 2026-10-12. This document records where JFrog publishes the API
definitions that this project mirrors, how they are organised, what can and cannot be
reused, and how the inventories in the rest of this research set were produced and
verified. It is the entry point for anyone adding or checking an endpoint. The endpoint
inventories themselves are in [02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md)
and [05-platform-identity-and-security-apis.md](05-platform-identity-and-security-apis.md).

## Where the definitions live

JFrog publishes an OpenAPI 3.1 definition for every REST endpoint, embedded inline on the
endpoint's documentation page under an "OpenAPI definition" heading. The pages are rendered
by a readme.io style platform and the embedded blocks carry `x-readme` extensions. There is
no single downloadable specification file. Source: <https://docs.jfrog.com/integrations/docs/openapi-specifications>.

Each documentation project exposes an `llms.txt` index that lists every page, and each
page is available as Markdown by appending `.md` to its URL. The site index is
<https://docs.jfrog.com/llms.txt>. The projects that matter for an artifact registry:

| Project | Index | API families |
|---|---|---|
| Binary Management (Artifactory) | <https://docs.jfrog.com/artifactory/reference/llms.txt> | Artifacts and Storage (30 pages), Package Type Specific (48), Repositories (13), Federated (25), Replication (19), Searches (20), Trash Can and GC (5), Release Bundles v1 (6), Retention v1 (8), Distribution (56) |
| Platform Administration | <https://docs.jfrog.com/administration/reference/llms.txt> | JFrog Platform REST APIs (Access: users, groups, permissions v2, tokens, roles, OIDC, SCIM, webhooks), Artifactory Security API (legacy `/api/security`), Artifactory System API, Import and Export, Loggers, Storage and Archive, AWS IAM, Access Federation |
| Project Management | <https://docs.jfrog.com/projects/reference/llms.txt> | Projects, project roles, members, repository attach and share, environments |
| Integrations | <https://docs.jfrog.com/integrations/reference/> | Webhooks, Live Logs, Plugins, the JFrog Go client |
| JFrog Security (Xray) | <https://docs.jfrog.com/security/reference/> | Scanning, watches, policies, violations, curation. Out of scope, see [10-scope-and-tiers.md](10-scope-and-tiers.md) |
| Governance (AppTrust) | <https://docs.jfrog.com/governance/reference/> | Release lifecycle, applications, evidence. Out of scope |

The Artifactory reference index contains 230 endpoint pages. The seven families this
project implements or shims were read page by page for
[02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md).

### Installation and configuration references

The YAML configuration descriptor that `PATCH /artifactory/api/system/configuration`
accepts, the bootstrap file and the configuration XSD are documented under the
Installation project, not the API reference:

- <https://docs.jfrog.com/installation/docs/artifactory-yaml-file-format.md>
- <https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/security-configurations-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/general-general-property-sets-proxy-mail-configurations-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/service-configurations-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/advanced-configurations-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md>
- <https://docs.jfrog.com/installation/docs/artifactory-bootstrap-yaml-file.md>
- <https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd>

The repository configuration JSON (the body of `PUT /api/repositories/{key}`) is
documented under Integrations: <https://docs.jfrog.com/integrations/docs/configuration-json-files.md>.

### Third-party mirror

<https://github.com/api-evangelist/jfrog> holds 53 refined OpenAPI files (`openapi/*.yml`,
declared as OpenAPI 3.2) assembled from JFrog's public pages. It has no licence file and
states "This is not our API". It was used only as a cross-check for path lists, because:

- The Artifactory files are thin: the Repositories file has 5 paths against 13 documented,
  Artifacts and Storage 8 against 30, Searches 5 against 20.
- The `jfrog-projects-api` file describes a different product (fields such as
  `devices_count`), the `jfrog-policies-api` file is Xray curation rather than retention,
  and the `jfrog-access-tokens-api` file invents `/access/api/v2/tokens` where the real
  path is `/access/api/v1/tokens`.

Nothing in this research set relies on the mirror alone.

### Client sources as a second authority

Where the documentation leaves a field untyped or a status code unstated, the clients that
must keep working were read instead, because they are what the server has to satisfy:

- JFrog CLI client library: <https://github.com/jfrog/jfrog-client-go> (`artifactory/services`, `access/services`, `auth`, `http/httpclient`) and <https://github.com/jfrog/jfrog-cli-core>.
- Terraform providers: <https://github.com/jfrog/terraform-provider-artifactory>, <https://github.com/jfrog/terraform-provider-platform>, <https://github.com/jfrog/terraform-provider-project> and the shared client <https://github.com/jfrog/terraform-provider-shared>.

These are summarised in [08-terraform-providers.md](08-terraform-providers.md) and
[09-jfrog-cli-compatibility.md](09-jfrog-cli-compatibility.md).

## Licence caveat

Every embedded JFrog definition declares `license: Proprietary` with JFrog's terms of
service as the URL. This repository therefore never vendors JFrog's specification files or
copies their descriptive prose. The research documents cite the page URLs, and the
project's own `api/*.openapi.yaml` files are authored from the documented behaviour. Path
templates, parameter names, JSON field names, enumerations and status codes are facts
about the wire protocol that clients depend on; they are recorded here in the project's
own words so the implementation stays interoperable.

## Conventions shared across the surface

### Base paths

One host serves several services under fixed prefixes. The Artifactory REST API is under
`https://{host}/artifactory/api`, package protocols and plain artifact paths under
`https://{host}/artifactory/`, identity under `https://{host}/access/api`, webhooks under
`https://{host}/event/api`, and health under `https://{host}/router/api/v1/system/health`.
The Terraform discovery document is at the host root, `https://{host}/.well-known/terraform.json`.
The JFrog Terraform providers discard any path in the configured URL and always re-add
these prefixes themselves (see [08-terraform-providers.md](08-terraform-providers.md)).

### Error envelopes

Each service has its own envelope, and the clients parse them differently:

| Surface | Envelope |
|---|---|
| Artifactory (`/artifactory/api/...`) | `{"errors":[{"status":404,"message":"..."}]}` |
| Access and Projects (`/access/api/...`) | `{"errors":[{"code":"...","message":"..."}]}` |
| OCI Distribution (`/v2/...`) | `{"errors":[{"code":"BLOB_UNKNOWN","message":"...","detail":{}}]}` |
| Xray (`/xray/api/...`) | `{"error":"..."}` |
| npm registry | `{"error":"..."}` |
| Terraform registry | `{"errors":["..."]}` |
| Go proxy, PyPI simple | plain text |
| NuGet | plain text or XML |

Source for the Artifactory and Access shapes: the `ErrorResponse` schema on any endpoint
page, for example <https://docs.jfrog.com/artifactory/reference/createorreplacepermissiontarget>.

### Authentication schemes

The embedded definitions declare three security schemes and most endpoints accept any of
them: `BasicAuth` (HTTP basic with a password, an access token or a reference token as the
password), `BearerAuth` (an access token or reference token), and `ApiKeyAuth` (header
`X-JFrog-Art-Api`, historically an API key, now a reference token since API keys reached
end of life; creation is disabled from 7.98.7, and from 7.111.4 an administrator blocks
existing keys with a platform setting that is off by default, per the
[7.98.7 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed) and [7.111.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed)). Anonymous access is permitted only for ping, readiness, liveness,
health and the OIDC token exchange unless anonymous access is enabled in the security
configuration. Details are in [05-platform-identity-and-security-apis.md](05-platform-identity-and-security-apis.md).

`GET /artifactory/api/system/version` requires a valid user, so a caller without
credentials is refused while anonymous access is off (**[J]** [RTFACT-26414](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26414),
"Anonymous users should not be able to access api/system/version end points", fixed in
7.35.1; [Get Artifactory Version](https://docs.jfrog.com/administration/reference/getartifactoryversion.md),
"Requires a valid user (can be anonymous)"). artifactweave answers 401 there, as it does on
every route outside the probes. Ping stays open: RTFACT-26414 names only version in its
summary and expected behaviour, and the
[ping reference](https://docs.jfrog.com/administration/reference/artifactoryping.md)
declares no security. While anonymous access is on, the sources disagree. The reference page
says "can be anonymous" and its OpenAPI block declares `security: []`; RTFACT-26414 asks that
anonymous users be refused even then; and the
[7.125.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)
fix "inconsistent token validation behavior ... when calling the system/version API with
anonymous access enabled". artifactweave answers the anonymous user there, as the
reference page says. That choice and the exact status for a caller without credentials are
**[I]** until the live comparison
([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

Sources: <https://docs.jfrog.com/administration/docs/access-tokens>,
<https://docs.jfrog.com/user-management/docs/api-key>,
<https://docs.jfrog.com/user-management/docs/identity-tokens>.

### Media types

Artifactory accepts vendor media types of the form
`application/vnd.org.jfrog.artifactory.<Family>.<Type>+json` alongside `application/json`
for request bodies, and some responses advertise them (for example
`application/vnd.org.jfrog.artifactory.repositories.RepositoryDetailsList+json`). The
AQL endpoint takes `text/plain`, the configuration PATCH takes `application/yaml`, and the
configuration GET returns `application/xml`.

## Research method

1. The `llms.txt` index of each family was fetched and every listed endpoint page was read
   as Markdown. The embedded OpenAPI block supplied method, path, server, parameters,
   defaults, enumerations, request and response schemas and status codes.
2. Field-level facts not covered by the OpenAPI blocks were taken from the configuration
   JSON and YAML pages, the format guides under <https://docs.jfrog.com/artifactory/docs/>,
   and the Terraform provider and CLI client sources.
3. Every claim was then challenged by an independent fact-check against the same primary
   sources plus the upstream protocol specifications (OCI, npm, GOPROXY, Maven, PEPs,
   NuGet, Helm, Terraform). Corrections were folded into the inventories; the review log in
   [13-review-log.md](13-review-log.md) records what changed.
4. A claim is marked *(unverified)* when no primary source confirms it: typically a value
   seen only in a community report, a client fixture or an older guide. Unverified values
   must be captured against a live Artifactory before they are hard-coded; the open list
   is in [12-open-questions.md](12-open-questions.md).
5. On 2026-10-17 (#342) the inventories were checked against the Artifactory 7.x release
   notes: the [self-managed notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases) (7.84.10 to 7.161.32) and the
   [SaaS notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases) (7.80.0 to 7.174.0). Notes for releases past their end of life are
   no longer published ([Artifactory End of Life](https://docs.jfrog.com/releases/docs/artifactory-end-of-life)),
   so earlier 7.x changes are covered only through the current reference pages. The
   deprecation pages were read too: [Artifactory deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations),
   [deprecations in process](https://docs.jfrog.com/releases/docs/deprecations-in-process),
   [JFrog Platform deprecations](https://docs.jfrog.com/releases/docs/jfrog-platform-deprecations)
   and [deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis).
   The order of sources is in the research README, under "Target version and sources".

## Parity oracle

The inventories in this set are a snapshot. To keep the authored specs honest as JFrog's
documentation moves, `tools/specoracle` re-reads the same indexes on demand and caches
the embedded OpenAPI block of every endpoint page under `.specoracle/` in the working
copy. That directory is gitignored: JFrog's definitions are proprietary and only ever
exist locally or in a CI cache, never in the repository.

`make oracle-fetch` fills the cache (658 pages and 648 OpenAPI blocks across the 22 families
listed in `tools/specoracle/main.go`, on 2026-10-18; the Builds family joined in
[#194](https://github.com/weaveplatform/artifactweave/pull/194)). `make parity` normalises both sides through one walker,
`internal/specoracle`, and compares every documented operation with
`api/*.openapi.yaml`: method and path (parameter names ignored), query and header
parameters and their enums, request media types and property paths, response status
codes and property paths. Descriptions are never compared and never stored.

The committed artefact is `api/parity-baseline.json`: the accepted list of missing
operations and divergent findings, naming only paths, field names and status codes.
`TestSpecParity` in `api/parity_test.go` skips without a cache and otherwise fails on
any gap not in the baseline. When an endpoint is implemented the test reports the
baseline entries that no longer apply, and `make parity-baseline` shrinks the file; the
diff is the parity progress for that change. The `parity` job in CI (see
[testing/ci.md](../testing/ci.md)) fetches on a cache miss and runs the test.

## Versions at the research baseline

| Component | Version | Source |
|---|---|---|
| Artifactory documentation | 7.x current pages, newest version gate seen 7.146 (Artifactory) and 7.155 (Access) | docs.jfrog.com |
| terraform-provider-artifactory | v12.11.14 (2026-09-11) | <https://github.com/jfrog/terraform-provider-artifactory/releases> |
| terraform-provider-platform | v2.2.11 (2026-08-25) | <https://github.com/jfrog/terraform-provider-platform/releases> |
| terraform-provider-project | v1.9.9 (2026-08-24) | <https://github.com/jfrog/terraform-provider-project/releases> |
| jfrog-client-go | v1.55.0 | <https://github.com/jfrog/jfrog-client-go> |
| jfrog-cli-core | v2.60.0 | <https://github.com/jfrog/jfrog-cli-core> |
| OCI distribution-spec | v1.1.1 | <https://github.com/opencontainers/distribution-spec/releases> |
