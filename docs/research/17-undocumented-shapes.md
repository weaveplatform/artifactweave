# Undocumented shapes: what public sources show

Research baseline: **2026-10-13**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); [the 7.x check](#artifactory-7x-check) records it.

JFrog's published definitions leave some request and response shapes untyped, and some
wire details are only visible on a live instance. This report records what public
sources show for each: JFrog's open-source SDK and CLIs, the Terraform providers, JFrog's
sample repositories, knowledge-base articles, archived documentation, and community
issues that paste real responses. Each finding carries a confidence:

- **Confirmed**: seen in a real Artifactory response or in a client's source code.
- **Likely**: consistent indirect evidence, no direct capture.
- **Guess**: no direct evidence; artifactweave's choice is stated.

Spec fragments under `api/src/` lose their `x-unverified` marker only where a finding is
confirmed. Field names, types and status codes are facts about the wire protocol; no
JFrog prose is reproduced.

## 1. Package protocol details

### Docker token challenge and token endpoint

| Detail | Finding | Confidence |
|---|---|---|
| Realm | `<base>/artifactory/api/docker/<repo-key>/v2/token` on the host the client addressed | Confirmed |
| Realm with no repository context | a bare `/v2/` on the repository-path method uses the literal key `null`: `.../api/docker/null/v2/token` | Confirmed |
| Older reverse-proxy setups | advertise `https://<host>/v2/token` | Confirmed |
| `service` | the hostname the client addressed, including a non-default port (`host%3A443` seen URL-encoded); never the repository key | Confirmed |
| `scope` | present on repository requests, absent on the bare `/v2/` probe | Confirmed |
| Token request | `GET` with Basic credentials and `account`, `client_id`, `offline_token`, `service`, `scope`; `POST` answers 405 with credentials and 401 without, and containerd falls back to `GET` | Confirmed |
| Anonymous | an anonymous `GET` returns a token when anonymous access is enabled | Confirmed |
| Token body | `{"token":"...","expires_in":3600}`; `access_token` and `issued_at` never seen | Confirmed |
| Default lifetime | 3600 seconds | Likely |

artifactweave sends `Bearer realm="<base>/artifactory/api/docker/<repo|null>/v2/token",service="<public host[:port]>"`
plus `scope` on repository requests, taking the host from the base-URL resolver. It
serves the token on `GET` at both the per-repository path (including `null`) and
`/v2/token`, answers `POST` with 405, ignores `account`, `client_id` and `offline_token`,
and returns `token`, `expires_in: 3600` and, harmlessly, `access_token` and `issued_at`
from the Docker token specification.

Sources: <https://github.com/moby/moby/issues/38662>,
<https://bugzilla.redhat.com/show_bug.cgi?id=1413987>,
<https://github.com/zed-industries/zed/issues/60458>,
<https://github.com/jfrog/artifactory-docker-examples/issues/86>,
<https://github.com/containerd/containerd/issues/2148>,
<https://jfrog.com/help/r/how-to-troubleshoot-docker-problems/docker-login-docker.art.local>,
<https://jfrog.atlassian.net/browse/RTFACT-26227> (search snippet).

### npm web login

The client contract is npm's own (`npm-profile`, `webAuth` and `webAuthCheckLogin`):
`POST <registry>/-/v1/login` with `{}` must return absolute `loginUrl` and `doneUrl`;
polling `doneUrl` gets 202 with `Retry-After` (a missing or non-numeric header makes
the client poll immediately) until 200 with `token`; any other status fails. Confirmed.
When the `POST` answers 4xx or 500, npm 12 raises `ENYI` and falls back to legacy login
(`PUT /-/user/org.couchdb.user:<user>`), so a registry without web login still works
without `--auth-type=legacy` ([research 25](25-npm.md) section 4; this corrects an earlier
claim that npm 9 and later refuse such registries). JFrog documents web login against `<base>/artifactory/api/npm/<repo>/` since
7.52 (confirmed); the URLs Artifactory returns are not captured and do not matter, since
npm treats them as opaque.

artifactweave: web login is deferred to #262, and until then `POST .../-/v1/login`
answers 404, so npm falls back to legacy login (decision
[0034](decisions/0034-npm.md#login-and-tokens)). The design #262 starts from:
`POST .../-/v1/login` opens a two-minute session and returns a `loginUrl` to a small
approval page and `doneUrl = <base>/artifactory/api/npm/<repo>/-/v1/done?sessionId=<id>`;
the done URL answers 202 with `Retry-After: 5` while pending, 200 `{token, username}` once
approved (single use) and 401 after expiry. With the page disabled the start call answers
404, as before #262.

Sources: <https://github.com/npm/npm-profile/blob/main/lib/index.js>,
<https://marmelab.com/blog/2022/12/22/how-to-implement-web-login-in-a-private-npm-registry.html>,
<https://verdaccio.org/docs/web-login>, <https://docs.jfrog.com/artifactory/docs/npm-repositories>,
<https://docs.jfrog.com/integrations/docs/jf-login>.

### PyPI

| Detail | Finding | Confidence |
|---|---|---|
| PEP 658 / 714 metadata | no evidence Artifactory serves `<file>.metadata` or `data-core-metadata`; it added an opt-in PEP 691 JSON index (7.139.1) and HTTP Range on downloads (7.128.0), which uv uses to read wheel metadata | Likely unsupported |
| Local storage layout | undocumented; enforce-layout checks only file name and version against metadata; public examples disagree | Inconclusive, `<name>/<version>/<file>` likely |
| Remote defaults | `url` `https://files.pythonhosted.org`, `pyPIRegistryUrl` `https://pypi.org`, suffix `simple` | Confirmed |

artifactweave implements the PEP 658 sidecars and both attributes, supports Range,
stores uploads at `<normalized-name>/<version>/<filename>` with `pypi.name`,
`pypi.version` and `pypi.summary` properties, and indexes by properties and file name so
the layout question does not affect clients.

Sources: <https://docs.jfrog.com/artifactory/docs/pypi-repositories>,
<https://docs.jfrog.com/releases/docs/artifactory-saas-releases> (search snippets),
<https://devopshq.github.io/artifactory/docs/FAQ.html>,
<https://github.com/jfrog/terraform-provider-artifactory> (`docs/resources/remote_pypi_repository.md`, `sample.tf`),
<https://github.com/jfrog/project-examples/blob/master/python-example/pip-example/README.md>.

### AQL `$rf`

jfrog-client-go never emits `$rf`; it uses `$match`, `$nmatch`, `$ne`, `$and`, `$or`,
`$asc` and `$desc` with the suffix order include, sort, offset, transitive, limit
(confirmed). The only sighting is the AQL syntax highlighter in jfrog-ui-essentials. No
AQL documentation page mentions it. artifactweave answers a query using `$rf` with 400
"unsupported AQL operator". Earlier research that said the CLI generates `$rf` was wrong.

Sources: <https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/utils/aqlquerybuilder.go>,
<https://github.com/jfrog/jfrog-ui-essentials/blob/master/src/configurations/codemirror.config.js>,
<https://docs.jfrog.com/artifactory/docs/aql-search-criteria>.

### Terraform discovery and the remote backend

A pasted Artifactory `/.well-known/terraform.json` shows absolute URLs: `modules.v1`
`https://<host>/artifactory/api/terraform/v1/modules/`; `state.v2`, `tfe.v2`, `tfe.v2.1`,
`tfe.v2.2` all `https://<host>/artifactory/api/terraform/remote/v2`; `login.v1` with
`client: terraform-cli`, `grant_types: [authz_code]`, `authz: https://<host>/ui/terraform/oauth2/authorize`,
`token: https://<host>/artifactory/api/oauth2/token` (confirmed; the capture predates
`providers.v1`). The backend is the Terraform Enterprise `remote` backend with
`organization` set to the backend repository key (confirmed); which go-tfe sub-paths
Artifactory serves is not captured. artifactweave emits `modules.v1`, `providers.v1`
(`/artifactory/api/terraform/v1/providers/`) and `login.v1` in that shape, and adds the
`state.v2` and `tfe.v2*` keys only if `terraformbackend` returns to scope.

Sources: <https://github.com/aquasecurity/trivy/discussions/7982>,
<https://github.com/hashicorp/terraform/issues/31950>,
<https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories>.

### Base URL behind proxies

| Detail | Finding | Confidence |
|---|---|---|
| Artifactory's base URL (OSS 6.20, `HttpUtils.getServletContextUrl`) | `X-JFrog-Override-Base-Url`, then `X-Artifactory-Override-Base-Url`, then the configured `urlBase` (given the request's scheme), then the servlet request's own server name and port. It never reads `X-Forwarded-Host` itself; a forwarded host reaches it only through what the servlet container or the front proxy makes of the request | Confirmed |
| A forwarded list | for `X-Forwarded-For`, Artifactory takes the first entry, the client's (`HttpUtils.getRemoteClientAddress`: it "might contain multiple entries") | Confirmed |
| `X-Forwarded-Host`, `X-Forwarded-Proto` and `X-Forwarded-Port` as lists | no public source shows Artifactory reading them as lists | Guess |
| `X-JFrog-Override-Base-Url` | honoured on every request when not blank, with no setting to turn it off: Artifactory returns it with a trailing slash and its own context path (`artifactory`) appended, so the header names the platform root. JFrog's current reverse-proxy guidance has the proxy set it (Reverse Proxy Settings, and the snippet HTTP Settings generates) | Confirmed |
| `X-Artifactory-Override-Base-Url` | honoured after it, returned as given ("original artifactory request url"), so it names the URL with the context path; JFrog's 4.4.0-era guidance made it mandatory for Docker | Confirmed |
| The configured `urlBase` | used with the servlet request's scheme put in place of its own when they differ | Confirmed |

artifactweave's resolver (decision 0002) reads the `X-Forwarded-*` headers itself. Each proxy in a
chain appends the value it received, so each header can arrive as a comma-separated list
(`X-Forwarded-Host: art.example.com, ingress.internal:8080`). artifactweave takes each header's
first entry, the client-facing hop, as Artifactory does for `X-Forwarded-For`, and ignores an
empty first entry rather than trusting a later, internal one (#249). It reads only the
`X-Forwarded-*` headers, not RFC 7239's `Forwarded`.

artifactweave follows OSS 6.20's order (#313, decision 0002): `X-JFrog-Override-Base-Url`, then
the configured `base_url`, then the forwarded headers, then the request. Until #313 it put the
forwarded headers ahead of the configured base URL, which let any client whose proxy passed on
its `X-Forwarded-Host` choose the URLs artifactweave hands out, the Docker token realm among
them. Two differences remain, both recorded in the deviations register: the legacy
`X-Artifactory-Override-Base-Url` is not honoured (DV-037), and a configured `base_url` is
used as written rather than with the request's scheme (DV-038). artifactweave's base URL is
the platform root, as `X-JFrog-Override-Base-Url` names it, so that header needs no context
path handling.

Sources: `base/common/src/main/java/org/artifactory/util/HttpUtils.java` in
knottnhupfer/artifactory-oss at `b99e5649bcad` (Artifactory OSS 6.20); JFrog's Reverse Proxy
Settings, <https://jfrog.com/help/r/jfrog-artifactory-documentation/reverse-proxy-settings>, and
HTTP Settings, <https://docs.jfrog.com/installation/docs/http-settings>; the 4.4.0 Docker article,
<https://jfrog.com/help/r/why-does-docker-stop-working-after-upgrading-to-artifactory-4-4-0/why-does-docker-stop-working-after-upgrading-to-artifactory-4.4.0>.

### Matrix parameter encoding

The JFrog CLI builds the matrix parameters of an upload (`jf rt u --props`, `--build-name`,
`--build-number`, `jf go-publish`) with jfrog-client-go's `Properties.ToEncodedString`, which
escapes each key and value with Go's `url.QueryEscape`. A space therefore travels as `+`
and a literal `+` as `%2B`; a comma inside a value is backslash-escaped before encoding, and
the comma between values is sent as `%2C` (confirmed).

Artifactory decodes each matrix *value* with `java.net.URLDecoder.decode(value, "UTF-8")`
in `ArtifactoryRequestBase.processMatrixParams`, which turns `+` into a space and `%2B`
into `+`; the key is not decoded (confirmed, OSS 6.20). The request path itself keeps a
literal `+` (`path.replace("+", "%2B")` before decoding). An unencoded `+` in a matrix value
is therefore a space on Artifactory too. artifactweave decodes values the same way (#260);
a key keeps a trailing `+` as the mandatory-property marker (`k+=v`).

Sources: <https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/utils/properties.go>,
<https://github.com/knottnhupfer/artifactory-oss/blob/master/base/api/src/main/java/org/artifactory/api/request/ArtifactoryRequestBase.java>.

### Still needs a live capture

Only the Terraform backend sub-paths (relevant only if `terraformbackend` returns) and,
optionally, the 600-second Docker token lifetime reported for access-token
authentication.

## 2. Access configuration, cleanup policies and webhooks

| Item | Finding | Confidence | Marker |
|---|---|---|---|
| OIDC `provider_type` | API values `Generic OpenID Connect`, `GitHub`, `GitHub Enterprise`, `Azure`; the platform Terraform provider maps its own `generic` and `GitHubEnterprise` onto them. artifactweave also accepts the lower-case forms older examples use and returns the canonical value | Confirmed | removed |
| OIDC `enable_permissive_configuration` | GitHub only (7.110.1+); when false, `organization` is mandatory and case-sensitive and only that organization's tokens are accepted; no effect for other provider types; default false | Confirmed | removed |
| Identity mapping `priority` | lower numbers are tried first; a mapping without a priority goes last; a `project_key` in the exchange puts that project's mappings before global ones; ties go to the earliest created; the provider requires at least 1 | Confirmed | removed |
| LDAP group `required_update` | `DOES_NOT_EXIST` (new, default on import), `REQUIRES_UPDATE` (the DN changed in LDAP), `IN_ARTIFACTORY` (up to date); assumes the Access service kept Artifactory's vocabulary | Confirmed | removed |
| Webhook signing | `X-JFrog-Event-Auth` carries the raw secret; with `use_secret_for_signing` the same header carries the lower-case hex HMAC-SHA256 of the raw body keyed by the secret | Confirmed | spec already correct |
| Troubleshooting `start` and `end` | Unix time, most likely milliseconds (the documented sample record's timestamp is in milliseconds). Modes: `start` alone streams stored records and stays live; `start` with `end` or `count` returns and ends; `start=0` reads from the oldest record. At most 10,000 records are kept, failures only unless the subscription is in debug mode | Likely (unit) | kept |
| Troubleshooting `response.body` | no documented limit; artifactweave keeps at most 4 KiB | Guess | kept |
| Cleanup runs view `sortBy`, `sortOrder` | newest first by default (`startTime`, `desc`); `sortBy` takes run-entry field names | Likely | kept |
| Cleanup runs view `page`, `pageSize` | undocumented; artifactweave uses 1-based `page` default 1 and `pageSize` 25, capped at 500 | Guess | kept |
| Cleanup `checkRepoAvailability` | only the name and default false are documented; artifactweave leaves repository keys that no longer exist out of the returned criteria and never changes the stored policy | Guess | kept |

Corrections made to the specs: the troubleshooting stream modes (the previous text said
`start` alone ends after the stored records); `provider_type` values are the capitalised
API names, not lower-case. Noted for the cleanup phase: JFrog writes cleanup run reports
into a `jfrog-policy-reports-local` repository and writes none when nothing matched; its
troubleshooting guide names the duration field `elapsed_time` while its sample and
OpenAPI block use `elapsed_millis`, which the spec follows.

To capture on a live instance: `GET /event/api/v1/troubleshooting?start=0&count=1` (unit
and truncation); a cleanup policy read with `checkRepoAvailability=true` naming a deleted
repository; the runs view with no paging parameters.

Sources: <https://raw.githubusercontent.com/jfrog/terraform-provider-platform/main/pkg/platform/resource_oidc_configuration.go>,
<https://docs.jfrog.com/administration/docs/openid-connect-integration.md>,
<https://docs.jfrog.com/administration/docs/ldap.md>,
<https://github.com/knottnhupfer/artifactory-oss/blob/master/base/common/src/main/java/org/artifactory/addon/ldapgroup/LdapUserGroup.java>,
<https://docs.jfrog.com/integrations/docs/predefined-webhooks.md>,
<https://github.com/akuity/kargo/blob/873b47f23fdb/pkg/webhook/external/artifactory.go>.

## 3. Workers and cold-storage archive

Research date 2026-10-13. Lane: Workers (`api/src/worker/`) and cold-storage archive
(`api/src/artifactory/paths-archive.yaml`). Confidence: **confirmed** = primary code or a
live server capture; **likely** = official documentation example or text; **guess** = our
design, no source.

### Sources

- JFrog CLI Workers plugin, github.com/jfrog/jfrog-cli-platform-services (main, cloned
  2026-10-13): `model/worker.go` (`WorkerDetails`, `Secret`, `Property`),
  `model/manifest.go` (`FilterCriteria`, `ArtifactFilterCriteria`,
  `ScheduleFilterCriteria`), `model/actions.go` (`ActionMetadata`, `FILTER_REPO`,
  `SCHEDULE`), `model/version.go`, `commands/dry_run_cmd.go` (`dryRunRequest`),
  `commands/execute_cmd.go`, `commands/show_execution_history_cmd.go`
  (`executionHistoryEntry`), `commands/list_cmd.go` (`getAllResponse`),
  `commands/deploy_cmd.go` (v2 create/update), `commands/common/cmd_api.go` (URL building,
  error handling), `commands/common/test_worker_server.go` (mock server structs),
  `commands/common/testdata/actions/*.json` (16 per-action metadata documents captured from
  a real server, with `typesDefinitions` and `samplePayload`), `test/commands/dry_run_cmd_test.go`
  and `execute_cmd_test.go` (integration assertions against a real platform),
  `test/infra/secrets_utils.go`, `test/infra/itest_runner.go`.
- JFrog worker samples, github.com/jfrog/workers-sample: 34 `manifest.json` files and
  their `types.ts`.
- terraform-provider-platform `pkg/platform/resource_workers_service.go` (v1 wire structs,
  mandatory-filter map).
- docs.jfrog.com: administration/docs/workers-limitations, optimize-workers-within-platform-limits,
  workers-troubleshooting, worker-event-types, worker-types, typescript-code-for-workers,
  workers-code-samples, installation/docs/worker-yaml-configuration,
  administration/docs/cold-storage, and the oracle blocks for createarchivepolicy and
  convertsearchcriteriatoaql.
- JFrog MCP server error handling (glama.ai/mcp/servers/@jfrog/mcp-jfrog, common/errors.ts).

### Items

#### WorkerTestRequest (`POST /worker/api/v1/test`, `/test/{workerKey}`)
- Shape: `{code: string, action: string, data: object, stagedSecrets?: [{key, value, markedForRemoval}], stagedProperties?: [same]}`; query `debug=true|false`, `projectKey`.
- Evidence: `dryRunRequest` in commands/dry_run_cmd.go (JSON tags); integration test `AssertSecretValueFromServer` posts `{code, action, data}` to `/worker/api/v1/test/<key>`.
- Confidence: confirmed. `x-unverified` removed.
- Note: `jf worker test-run` always calls the keyed form with the local code; the CLI's integration runner also uses `POST /worker/api/v2/test/<key>` with `action: {application, name}` (not in the oracle).

#### Test and execute responses
- Execute: `{"data": <script return>, "executionStatus": "STATUS_SUCCESS"}`. Test: keyed by action in lower camel case, `{"genericEvent": {"data": ..., "executionStatus": "STATUS_SUCCESS"}}`.
- Evidence: execute_cmd_test.go `assertExecuteSucceed`; dry_run_cmd_test.go asserts `gotResponse["genericEvent"]`; workers-code-samples responses show `executionStatus: "STATUS_SUCCESS"`.
- Confidence: confirmed for success and for the GENERIC_EVENT key; the lower-camel key for other actions is likely; the failure `executionStatus` value is unpublished (we write `STATUS_FAILURE`, still `x-unverified`).
- Spec: new `WorkerTestResult` (map of `WorkerExecutionResult`), `WorkerExecutionResult` gains `data`, `executionStatus`, `errorCode`, `errorMessage`.

#### WorkerExecutionHistoryEntry (`GET /worker/api/v1/execution_history`)
- Shape: bare array of `{workerKey, workerType, workerProjectKey, executionStatus, startTimeMillis, endTimeMillis, triggeredBy, testRun, executedVersion, traceId}`; query `workerKey`, `showTestRun`, `projectKey` (plus the oracle's `maxItems`, `start`, `end`).
- Evidence: `executionHistoryEntry` in show_execution_history_cmd.go and `ExecutionHistoryEntryStub` in test_worker_server.go.
- Confidence: confirmed. Replaced our invented entry (`id`, `action`, `status`, `startedAt`, `durationMillis`, `logs`, `payload`, `result`, `error`). `executionStatus` failure values still unverified; `errorCode`/`errorMessage` are artifactweave additions.

#### Worker definition (create, update, get)
- Shape (v1): `{key, description, enabled, debug, sourceCode, action: string, filterCriteria, secrets: [{key, value, markedForRemoval}], properties: [same], projectKey, shared}`; `sourceCode` plain or `base64:<encoded>`.
- Evidence: `WorkerDetails` (CLI), terraform-provider-platform API model, oracle create request.
- Confidence: confirmed. Added `secrets`, `properties`, `projectKey` (were missing).
- filterCriteria: `artifactFilterCriteria {repoKeys, anyLocal, anyRemote, anyFederated, includePatterns, excludePatterns}`, `schedule {cron, timezone}`. Confirmed (CLI manifest model, provider). Added `schedule`.
- v2 (`/worker/api/v2/workers`, used by `jf worker deploy`): `action` is `{application, name}` and a `version {commitSha, description, versionNumber}` object exists. Not in the oracle, not specified.

#### ActionMetadata (`GET /worker/api/v2/actions`)
- Shape: `{action: {application, name}, description, samplePayload, sampleCode, typesDefinitions, supportProjects, filterType: FILTER_REPO|SCHEDULE, mandatoryFilter, wikiUrl, async, executionRequestType}`.
- Evidence: `ActionMetadata` in model/actions.go; 16 captured documents.
- Confidence: confirmed. `filterType` now an enum. Our `requestSchema` field has no source (left as an optional extra).

#### Action catalogue (`action` enum)
- The oracle list (44) differs from live names: a live catalogue and the samples use `BEFORE_REMOTE_INFO`, `AFTER_WORKLOAD_STATE_CHANGE` and `SCHEDULED_EVENT`; the oracle has `BEFORE_REMOTE_INFO_REQUEST`, `AFTER_WORKLOAD_STATE_CHANGE_REQUEST`, `AFTER_SAVE`, `BEFORE_UPLOAD_REQUEST`, which appear in no sample or capture.
- Spec: enum is now the union (47); the description names the four reference-only values as accepted aliases.
- Confidence: confirmed for the three added names; the four reference-only names are guesses kept for parity.

#### /worker ErrorResponse
- No source shows a Workers error body: the CLI prints it raw (`printJSONOrLogError`), the platform provider reports `response.String()`, and JFrog's MCP client reads `errors[0].message` then `message`.
- Confidence: guess. `x-unverified` kept; description now records the evidence and the tolerance.

#### Limits and failure codes (coordinator's design change)
- JFrog defaults (worker-yaml-configuration, workers-limitations): `cpuTimeMillis` 1000, `callTimeMillis` 5000, `memoryMB` 64, `maxCallDepth` 100, `jsonPayloadLimitKB` 100, `maxCodeChars` 100000, SaaS max execution 20 s, **`maxWaitingInQueueMillis` 30000**.
- JFrog publishes failure messages, not codes (optimize-workers-within-platform-limits): `Worker used too much memory`, `Worker used too much cpu time`, `Worker execution timeout`, `Worker waited for too long in the queue`, `Worker pool rejected the task as it is in closing state`, `The worker has been killed unexpectedly`, `Floating promises are not allowed. Make sure to await all promises`.
- Spec: artifactweave codes `WORKER_MEMORY_LIMIT`, `WORKER_CPU_LIMIT`, `WORKER_EXECUTION_TIMEOUT`, `WORKER_QUEUE_TIMEOUT`, `WORKER_KILLED`, `WORKER_FLOATING_PROMISE`, `WORKER_SCRIPT_ERROR`, `WORKER_PAYLOAD_TOO_LARGE`, each mapped to JFrog's message text where one exists; added to `WorkerExecutionResult` and `WorkerExecutionHistoryEntry`. Limits text added to the worker definitions, execute, both test operations and execution history; "no memory cap" removed.
- Discrepancy: the coordinator's note gives a 5 s queue wait, but JFrog's documented default is 30 000 ms. The spec says 30 s; ADR 0026 should match one or the other.
- Other runtime facts for P17 (typescript-code-for-workers, workers-limitations): `PlatformContext {baseUrl, clients {platformHttp, axios}, platformToken, wait(ms), state, getRemainingExecutionTime(), crypto {hash, hmac, randomBytes}}`; imports and timers are disallowed; JFrog's own sample returns `status: 'DOWNLOAD_PROCEED'` as a string while the type definitions use numeric enums, so the runtime must accept both; failed runs trigger exponential backoff (max 17 min, block after 10 failures in 10 min, reset daily; queue-wait failures and test runs exempt).

#### ArchiveSearchCriteriaForm (cold storage, non-package archive policy)
- JFrog documents the UI fields (cold-storage page, "Create an Archive Policy" for artifacts): include/exclude repository, include/exclude path pattern, include/exclude artifact name pattern, include/exclude property name and value, each with an And/Or toggle, plus Deployed before and Last downloaded before. The JSON names are not published anywhere found (oracle declares an empty object; web search found none).
- Spec: structured with artifactweave names (`includedRepositories`, `excludedRepositories`, `includedPathPatterns`, `excludedPathPatterns`, `includedNamePatterns`, `excludedNamePatterns`, `includedProperties`, `excludedProperties` as `{values|properties, operator: OR|AND}`, `deployedBeforeInDays`, `lastDownloadedBeforeInDays`); `additionalProperties` kept so a real Artifactory form round-trips.
- Confidence: field set likely, names guess. `x-unverified` kept.

#### POST /artifactory/api/retention/archive/policies/convertToAql
- Shape: request `text/plain` whose body is the form's JSON text; response `application/json` string holding the AQL.
- Evidence: oracle block (convertsearchcriteriatoaql).
- Confidence: confirmed for the wire format. `x-unverified` removed from the operation (the form's contents stay unverified on the schema).

### Worker action table

Payload and return types are JFrog's TypeScript names; statuses exclude the `*_UNSPECIFIED` and `UNRECOGNIZED` sentinels. Numeric values follow declaration order (PROCEED = 1, STOP = 2, WARN = 3).

| Action | Application | Payload type | Return type | Return fields | Allowed statuses | Filter | Async | Source |
|---|---|---|---|---|---|---|---|---|
| `AFTER_BUILD_INFO_SAVE` | artifactory | `AfterBuildInfoSaveRequest` | `AfterBuildInfoSaveResponse` | `message` | none (no status; message only) | - | yes | live catalogue (CLI testdata) + doc sample |
| `AFTER_COPY` | artifactory | `AfterCopyRequest` | `AfterCopyResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_CREATE` | artifactory | `AfterCreateRequest` | `AfterCreateResponse` | `message` | none (no status; message only) | FILTER_REPO | yes | live catalogue (CLI testdata) + doc sample |
| `AFTER_DELETE` | artifactory | `AfterDeleteRequest` | `AfterDeleteResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_DOWNLOAD` | artifactory | `AfterDownloadRequest` | `AfterDownloadResponse` | `message` | none (no status; message only) | FILTER_REPO | yes | live catalogue (CLI testdata) + doc sample |
| `AFTER_DOWNLOAD_ERROR` | artifactory | `AfterDownloadErrorRequest` | `AfterDownloadErrorResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_MOVE` | artifactory | `AfterMoveRequest` | `AfterMoveResponse` | `message` | none (no status; message only) | FILTER_REPO | yes | live catalogue (CLI testdata) + doc sample |
| `AFTER_PROPERTY_CREATE` | artifactory | `AfterPropertyCreateRequest` | `AfterPropertyCreateResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_PROPERTY_DELETE` | artifactory | `AfterPropertyDeleteRequest` | `AfterPropertyDeleteResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_REMOTE_DOWNLOAD` | artifactory | `AfterRemoteDownloadRequest` | `AfterRemoteDownloadResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_REPO_CREATE` | artifactory | `AfterRepoCreateRequest` | `AfterRepoCreateResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_REPO_DELETE` | artifactory | `AfterRepoDeleteRequest` | `AfterRepoDeleteResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_REPO_UPDATE` | artifactory | `AfterRepoUpdateRequest` | `AfterRepoUpdateResponse` | `message` | none (no status; message only) | - | yes | doc sample |
| `AFTER_WORKLOAD_STATE_CHANGE` | runtime | `AfterWorkloadStateChangeRequest` | `AfterWorkloadStateChangeResponse` | - | none (no status; message only) | - | yes | live catalogue (CLI testdata) + doc sample |
| `ALT_ALL_RESPONSES` | artifactory | `AltAllResponsesRequest` | `AltAllResponsesResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `ALT_REMOTE_CONTENT` | artifactory | `AltRemoteContentRequest` | `AltRemoteContentResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `ALT_RESPONSE` | artifactory | `AltResponseRequest` (unverified) | `AltResponseResponse` (unverified) | `message` likely | PROCEED, STOP, WARN likely (shared ActionStatus, as the other ALT_ actions) | - | no | event-types page only; no code sample or capture |
| `ALT_REMOTE_PATH` | artifactory | `AltRemotePathRequest` | `AltRemotePathResponse` | `message`, `modifiedRepoPath` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_BUILD_INFO_SAVE` | artifactory | `BeforeBuildInfoSaveRequest` | `BeforeBuildInfoSaveResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_COPY` | artifactory | `BeforeCopyRequest` | `BeforeCopyResponse` | `message`, `executionStatus`, `status` | PROCEED, STOP, WARN | - | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_CREATE` | artifactory | `BeforeCreateRequest` | `BeforeCreateResponse` | `message`, `status` | PROCEED, STOP, WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_CREATE_TOKEN` | access | `BeforeCreateTokenRequest` | `BeforeCreateTokenResponse` | `status`, `message` | CREATE_TOKEN_PROCEED, CREATE_TOKEN_STOP, CREATE_TOKEN_WARN | - | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_DELETE` | artifactory | `BeforeDeleteRequest` | `BeforeDeleteResponse` | `status`, `message` | BEFORE_DELETE_PROCEED, BEFORE_DELETE_STOP, BEFORE_DELETE_WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_DELETE_REPLICATION` | artifactory | `BeforeDeleteReplicationRequest` | `BeforeDeleteReplicationResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_DIRECTORY_REPLICATION` | artifactory | `BeforeDirectoryReplicationRequest` | `BeforeDirectoryReplicationResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_DOWNLOAD` | artifactory | `BeforeDownloadRequest` | `BeforeDownloadResponse` | `status`, `message` | DOWNLOAD_PROCEED, DOWNLOAD_STOP, DOWNLOAD_WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_DOWNLOAD_REQUEST` | artifactory | `BeforeDownloadRequestRequest` | `BeforeDownloadRequestResponse` | `message`, `modifiedRepoPath` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_FILE_REPLICATION` | artifactory | `BeforeFileReplicationRequest` | `BeforeFileReplicationResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_MOVE` | artifactory | `BeforeMoveRequest` | `BeforeMoveResponse` | `message`, `status` | PROCEED, STOP, WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_PROPERTY_CREATE` | artifactory | `BeforePropertyCreateRequest` | `BeforePropertyCreateResponse` | `status`, `message` | BEFORE_PROPERTY_CREATE_PROCEED, BEFORE_PROPERTY_CREATE_STOP, BEFORE_PROPERTY_CREATE_WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_PROPERTY_DELETE` | artifactory | `BeforePropertyDeleteRequest` | `BeforePropertyDeleteResponse` | `status`, `message` | BEFORE_PROPERTY_DELETE_PROCEED, BEFORE_PROPERTY_DELETE_STOP, BEFORE_PROPERTY_DELETE_WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_PROPERTY_REPLICATION` | artifactory | `BeforePropertyReplicationRequest` | `BeforePropertyReplicationResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_REMOTE_DOWNLOAD` | artifactory | `BeforeRemoteDownloadRequest` | `BeforeRemoteDownloadResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_REMOTE_INFO` | artifactory | `BeforeRemoteInfoRequest` | `BeforeRemoteInfoResponse` | `status`, `message`, `executionStatus`, `requestHeaders` | PROCEED, STOP, WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_REPO_CREATE` | artifactory | `BeforeRepoCreateRequest` | `BeforeRepoCreateResponse` | `message`, `status` | BEFORE_REPO_CREATE_PROCEED, BEFORE_REPO_CREATE_STOP, BEFORE_REPO_CREATE_WARN | - | no | doc sample |
| `BEFORE_REPO_DELETE` | artifactory | `BeforeRepoDeleteRequest` | `BeforeRepoDeleteResponse` | `message`, `status` | BEFORE_REPO_DELETE_PROCEED, BEFORE_REPO_DELETE_STOP, BEFORE_REPO_DELETE_WARN | - | no | doc sample |
| `BEFORE_REPO_UPDATE` | artifactory | `BeforeRepoUpdateRequest` | `BeforeRepoUpdateResponse` | `message`, `status` | BEFORE_REPO_UPDATE_PROCEED, BEFORE_REPO_UPDATE_STOP, BEFORE_REPO_UPDATE_WARN | - | no | doc sample |
| `BEFORE_REVOKE_TOKEN` | access | `BeforeRevokeTokenRequest` | `BeforeRevokeTokenResponse` | `status`, `message` | REVOKE_TOKEN_PROCEED, REVOKE_TOKEN_STOP, REVOKE_TOKEN_WARN | - | no | live catalogue (CLI testdata) + doc sample |
| `BEFORE_STATISTICS_REPLICATION` | artifactory | `BeforeStatisticsReplicationRequest` | `BeforeStatisticsReplicationResponse` | `message` | PROCEED, STOP, WARN | - | no | doc sample |
| `BEFORE_TOKEN_EXPIRY` | access | `BeforeTokenExpiryRequest` | `BeforeTokenExpiryResponse` | `message` | CREATE_TOKEN_PROCEED | - | no | doc sample |
| `BEFORE_UPLOAD` | artifactory | `BeforeUploadRequest` | `BeforeUploadResponse` | `status`, `message`, `modifiedRepoPath` | UPLOAD_PROCEED, UPLOAD_STOP, UPLOAD_WARN | FILTER_REPO | no | live catalogue (CLI testdata) + doc sample |
| `GENERIC_EVENT` | worker | `GenericEventRequest` | `(free-form return value)` | - | none | - | no | live catalogue (CLI testdata) + doc sample |
| `SCHEDULED_EVENT` | worker | `ScheduledEventRequest` | `ScheduledEventResponse` | `message` | none (no status; message only) | SCHEDULE | yes | live catalogue (CLI testdata) + doc sample |

Notes: `BEFORE_UPLOAD`, `BEFORE_DOWNLOAD_REQUEST` and `ALT_REMOTE_PATH` may return `modifiedRepoPath`; `BEFORE_REMOTE_INFO` returns `requestHeaders` and an optional `executionStatus`; JFrog's `BEFORE_TOKEN_EXPIRY` sample returns `CREATE_TOKEN_PROCEED`, which looks like a documentation slip (its own enum is unpublished). Filter requirements beyond the captured 16 come from the platform provider's map: `AFTER_PROPERTY_CREATE` and `AFTER_PROPERTY_DELETE` need FILTER_REPO, `AFTER_BUILD_INFO_SAVE` rejects a filter.

### Contradictions with our previous spec

1. Execution history entry fields were invented; replaced with the CLI's sourced shape.
2. Test responses are keyed by action (`genericEvent`), not the bare result.
3. `/test/{workerKey}` runs the supplied code with the saved worker's secrets, not the saved code.
4. Worker definitions lacked `secrets`, `properties`, `projectKey`; filter criteria lacked `schedule`.
5. Action enum lacked `SCHEDULED_EVENT`, `BEFORE_REMOTE_INFO`, `AFTER_WORKLOAD_STATE_CHANGE`.
6. Unresolved: `GET /worker/api/v1/workers` is a bare array in the oracle (kept, for parity) but `jf worker list` decodes `{"workers": [...]}`; the JSON output path passes either through, the table output prints nothing for an array. Needs a live capture; if the object form is real, the spec and the parity baseline need a deliberate exception.

Decisions taken on these findings: the Workers queue wait follows the JFrog default of
30 seconds (decision 0026 and the configuration reference were corrected from 5); the
`errorCode` values in execution results and history (`WORKER_MEMORY_LIMIT`,
`WORKER_CPU_LIMIT`, `WORKER_EXECUTION_TIMEOUT`, `WORKER_QUEUE_TIMEOUT`, `WORKER_KILLED`,
`WORKER_FLOATING_PROMISE`, `WORKER_SCRIPT_ERROR`, `WORKER_PAYLOAD_TOO_LARGE`) are
artifactweave's, since JFrog publishes failure messages but not codes. Two client facts
change P17: `jf worker list` decodes `{"workers": [...]}` although the published
definition shows a bare array (needs a live capture; the spec keeps the published form),
and `jf worker deploy` uses `/worker/api/v2/workers` (with `action` as
`{application, name}` and a `version` object) and the v2 test endpoint, neither of which
is in the published definitions, so P17 specifies them from the CLI source.

## 4. Federation, topology, pairing, locks and replication

| Item | Finding | Confidence | Marker |
|---|---|---|---|
| `POST /api/federation/reconnect` | in Artifactory it reconnects to the standalone Federation Service over gRPC (a health job also does it every 80 seconds); 424 means that service is unreachable. In artifactweave it restarts the in-process sync worker and 424 means it could not start | Confirmed | removed |
| `GET /api/federation/status/connectionStates/{repoKey}` `status` | enum `HEALTHY`, `DELAYED`, `ERROR`, `PENDING_FS`, `FULL_SYNC_RUNNING`, `DISABLED`, `UNSUPPORTED`, `READONLY`, `N/A`; Federation Service only in Artifactory (405 otherwise) | Confirmed | removed |
| Locks (`ClusterLocksResponse`, `NodeLocks`, `LockEntry`) | our schemas match the documented shape field for field; Artifactory serves them from 7.161, above the 7.155 artifactweave reports | Confirmed | removed |
| Pairing token | response `{pairing_token}` only; a signed JWT, single use, 300 seconds by default, subject is the requesting principal | Confirmed | removed |
| `POST /api/replication/execute/{repoPath}` | 200 with no body; the run is scheduled and its messages go to the service log. `strategy` values are not published | Confirmed (response); unknown (`strategy`) | kept on `strategy` only |
| Grid `DELETE /api/v1/topology/{key}` 409 | federation cleanup failed so the topology is kept; the body is the stored topology with failed sites `Disconnected` and the reason in `status_description`; the client retries | Confirmed | removed |
| Federation repository status v1 and v2 | top level confirmed (`localKey`, `binariesTasksInfo{inProgressTasks, failingTasks}`, `mirrorEventsStatusInfo[]`, `federatedArtifactStatus`); mirror records typed from the legacy status view (`lagInMS`, `eventRegistrationTimeStamp` likely; event counters and the v1 status strings `SYNCED`/`OUT_OF_SYNC` guessed); `mirrorEventsStatusInfo[].status` is read by operator scripts | Partly confirmed | kept per inferred property |
| Recovery `force` | only "forces recovery" is documented; the described behaviour is artifactweave's | Guess | kept |
| Grid topology key | derived from the name; the rule (lower-case, non-alphanumeric runs to hyphens) is artifactweave's | Guess | kept |

Corrections made to the specs: replication execute answered 202 and now answers 200 with
no body; the topology-delete 409 means failed cleanup, not a change in progress;
reconnect concerns the federation service link, not peer connections. Decision 0021,
research 15 and the Access pairing operation were aligned to the 300-second token
lifetime. JFrog's own samples disagree with its reference schemas in two places
(`GET /api/federation/state` uses `localRepoKey` where the schema says `key`; `stateList`
member flags appear as `supported`, `disabled`, `inLag` where the schema says
`isSupported`, `isDisabled`, `isInLag`); the spec follows the schema until a live capture
decides. The member-state, queue, heartbeat, full-sync phase and binary-task enums and the
retry and auto-healing defaults in JFrog's member-state reference and recovery pages are
inputs for P13.

Sources: <https://docs.jfrog.com/artifactory/docs/recovery-and-auto-healing.md>,
<https://docs.jfrog.com/artifactory/docs/member-state-reference.md>,
<https://docs.jfrog.com/artifactory/docs/monitoring-connection-states.md>,
<https://docs.jfrog.com/artifactory/docs/legacy-ui-monitoring.md>,
<https://docs.jfrog.com/installation/docs/pairing-tokens.md>,
<https://github.com/nagarajuv-jfrog/openapi> (commit a0d23c96),
<https://github.com/sachi7899/SimpleD1>, and the cached reference blocks for the locks,
topology and replication operations.

## 5. Distribution and release bundles v1

The strongest sources here are jfrog-client-go's Distribution structs and archived copies
of JFrog's old Distribution and Artifactory REST pages, which carried full JSON examples;
current documentation pages carry one-line summaries only.

| Item | Finding | Confidence | Marker |
|---|---|---|---|
| Release bundle create body | `name`, `version`, `dry_run`, `sign_immediately`, `storing_repository` (nullable), `description`, `release_notes {syntax, content}`, `spec {queries[{aql, query_name, mappings, added_props, exclude_props_patterns}], exclude_props_patterns, source_artifactory_id}`. Create answers 201 (200 on a dry run, 409 when the version exists); when signed in the same call the manifest SHA-256 comes back in `X-Checksum-Sha256`. Update sends the same body without `name` and `version` and answers 200 | Confirmed | removed |
| Version state | the field is `state` with `OPEN` and `SIGNED`; the current reference block names it `status`, so artifactweave sends both with the same value. Versions carry `created_by`, `distributed_by`, `archived`; artifacts carry `source_repo_path`, `target_repo_path`, `xray_scan_info` | Confirmed (`state`); `status` alias kept unverified | removed except on `status` |
| Distribution status entries | `distribution_id`, `distribution_friendly_id`, `type` (`distribute`, `delete_release_bundle_version`), bundle name and version, `created_by`, `distributed_by`, `start_time`, `finish_time`, `duration`, `distribution_rules`, `sites[]` with `status`, `general_error`, `target_artifactory {service_id, name, type}`, file and byte counters, `file_errors`, `files_in_progress`. Statuses `Not distributed`, `In progress`, `In queue`, `Completed`, `Failed` | Confirmed | removed |
| Propagation report | `{report: {message, status, details[{jpd_id, name, key_alias, status}]}}` with six status values | Confirmed | removed |
| GPG key alias list | items `{alias, default, passphrase}` | Likely (one archived example) | kept |
| Artifactory bundle listings | shape confirmed; `COMPLETE` is the only documented status, artifactweave adds `IN_PROGRESS` and `FAILED` | Partly confirmed | kept on the status values |
| Artifactory version `format` | `jws` or `json`; `jws` returns a compact JWS whose payload carries `id, name, version, created, description, release_notes, type, signature, artifacts[{repo_path, checksum, download_token}]`; the `json` default is likely only | Partly confirmed | kept on the default |
| Import status | enum `IN_PROGRESS`, `NOT_EXPORTED`, `FAILED`, `COMPLETED`; `NOT_EXPORTED` most likely means no import has started | Partly confirmed | kept on the meaning |

Corrections made to the specs: the invented `READY_FOR_DISTRIBUTION` state is gone (also
removed from `pkg/types`, which gained `In queue`); create answers 201; distribution
status gained timing, sites and rules; the propagation report is wrapped in `report`; the
alias list uses `default`, not `is_default`; the Distribution `format` values were
swapped (now `json` default, `jws` as `application/jose+json` with header, payload and
signature, `jose` compact), while Artifactory's `jws` is the compact form; signing an
already signed version is 400, sign accepts an optional `{storing_repository}` and
returns `X-Checksum-Sha256`; key upload and alias rename accept both the wrapped 2.4+
form (`{key{...}, propagate_to_edge_nodes, fail_on_propagation_failure, set_as_default}`,
`new_alias`) and the flat form the current reference shows, and upload answers with the
propagation report.

For P15: jfrog-client-go parses the older status object from the v1 tracker-id status
URL while the current reference documents `DistributionProgress` there, so the handler
returns the union of both so `jf ds` status output and the documented form both work.

Sources: <https://github.com/jfrog/jfrog-client-go> (`distribution/services/{create,update,sign,getstatus}.go`,
`distribution/services/utils/releasebundlebody.go`, `utils/distribution/distribute.go`,
`artifactory/services/utils/releasebundleutils.go`),
<https://web.archive.org/web/20210623060352/https://www.jfrog.com/confluence/display/JFROG/Distribution+REST+API>,
<https://web.archive.org/web/20230128035857/https://www.jfrog.com/confluence/display/JFROG/Distribution+REST+API>,
<https://web.archive.org/web/20211204064204/https://www.jfrog.com/confluence/display/JFROG/Artifactory+REST+API>.

## Summary

Markers counted on 2026-10-13. Where a shape was only partly confirmed, the marker moved
from the whole schema or operation onto the individual inferred fields, so a lane can end
with more, narrower markers than it started with.

| Lane | Marked before | Marked after |
|---|---|---|
| Distribution and release bundles v1 | 9 schemas or operations | 5 fields or parameters |
| Workers and cold storage | 4 schemas or operations | 4 fields or schemas |
| Federation, topology, pairing, locks, replication | 14 schemas or operations | 25 fields (mostly federation status mirror records) |
| Access configuration, cleanup, webhooks | 11 fields or parameters | 7 fields or parameters |
| Package protocols (research docs, not specs) | 6 capture items | 1 (Terraform backend sub-paths, only if that feature returns) |

Captures still worth taking on a live instance, in priority order:

1. `GET /worker/api/v1/workers` (bare array or `{"workers": [...]}`) and the v2 Workers
   endpoints `jf worker deploy` uses.
2. `GET /api/federation/status/repo/{repoKey}` (v1 and v2) for the mirror record fields,
   and `GET /api/federation/state` and `stateList` for the field-name disagreements.
3. The Distribution v1 tracker status body as a real server returns it.
4. `GET /event/api/v1/troubleshooting?start=0&count=1` for the timestamp unit.
5. The cleanup runs view with no paging parameters, and a policy read with
   `checkRepoAvailability=true`.

## Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation.

**Confirmed for 7.x:**
- **Base-URL headers.** 7.x still honours both base-URL override headers:
  - [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) fixed `X-JFrog-Override-Base-URL` being ignored during `npm install` from a
    virtual repository;
  - [7.133.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-713310-self-managed) (RTDEV-73702) fixed npm virtual metadata ignoring
    `X-Artifactory-Override-Base-Url`.

  Their order of precedence is still 6.20's (DV-037) and stays on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- **Locks from 7.161.** The locks operations are served from 7.161, as section 4 says.
- **PyPI JSON index.** The PyPI Simple JSON index is opt-in through `Accept` (self-managed
  [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)).
- **Federation.** Its auto-healing retries failed binary tasks ([7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed)), and the replay
  and delete failed binary tasks operations are in the current reference. That agrees with
  the binary-task fields of section 4.
- **Distribution v1.** Its Replicator is deprecated from 7.84.10 ([deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations)).
  Distribution itself continues as section 5 describes.

**Unconfirmed for 7.x:**
- the base-URL precedence and the matrix-parameter key decoding (section 3);
- the LDAP group shape, which come from the 6.20 source;
- every item marked Guess or Likely above.

They stay on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
