# 06: System API and the configuration descriptor

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)). **[KB]** marks
a JFrog KB article and **[J]** a JFrog Jira issue, as in the
[research README](README.md#target-version-and-sources).

The system endpoints every client touches (ping, version, health, telemetry), the inputs to
the version-string decision, and the Artifactory configuration descriptor in its YAML and
XML forms. The YAML descriptor is the configuration-as-code format artifactweave accepts on
`PATCH /api/system/configuration` and through `artifactweave config apply`
([ADR 0009](decisions/0009-configuration-as-code.md)).

Research baseline: **2026-10-12**. Sources: the per-endpoint pages under
https://docs.jfrog.com/administration/reference/jfrog-platform-artifactory-system-api/llms.txt
and https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt, the
installation docs on YAML configuration, the Terraform provider and JFrog CLI sources.
Items marked *(unverified)* have no primary-source confirmation. Other claims carry a label
where the source matters:

| Label | Means |
|---|---|
| **[D]** | documented at the cited JFrog page |
| **[S]** | seen in a JFrog client's source at the cited tag |
| **[J]** | JFrog's public Jira, under the [rules for Jira claims](README.md#target-version-and-sources) |
| **[I]** | inferred; no source states it |

## System endpoints

The status column is the Artifactory compatibility contract. Notes identify current
artifactweave gaps and their owners in the
[capability roadmap](../implementation/capability-roadmap.md); a generated route or
schema does not establish the successful behavior. P20 maintains the operation-level
inventory. Hosted or commercial APIs remain planned integrations, subject to observed
contracts and provider access.

| Method | Path | Auth | Status | Notes |
|---|---|---|---|---|
| GET | `/artifactory/api/system/ping` | anonymous | 200 / 500 | `text/plain` body `OK`; 500 "Internal Server Error" when Artifactory is not working ([ping](https://docs.jfrog.com/administration/reference/artifactoryping.md)). The 6.20 `PingResource` checks storage (`StorageService.ping()`) and Access (`AccessService.ping()`) and answers 500 with the reason as the body; that 7.x checks the same is **[I]** |
| GET | `/artifactory/api/v1/system/readiness` | anonymous | 200 / 500 | `{"code":"OK"}`; 500 "with reason if not working properly" ([readiness](https://docs.jfrog.com/administration/reference/getreadinessprobe.md)). The failure body is **[I]**: no page shows one |
| GET | `/artifactory/api/v1/system/liveness` | anonymous | 200 / 500 | `{"code":"OK"}`; 500 as for readiness ([liveness](https://docs.jfrog.com/administration/reference/getlivenessprobe.md)). What liveness checks is **[I]** |
| GET | `/artifactory/api/system/version` | any user; anonymous only while anonymous access is on | 200 / 401 | 401 without credentials while anonymous access is off (**[J]** [RTFACT-26414](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26414), fixed in 7.35.1; the status and the anonymous-on case are **[I]**, see [research 01](01-jfrog-api-landscape.md#authentication-schemes)). `{"version":"...","revision":"...","addons":[]}`; also served as `application/vnd.org.jfrog.artifactory.system.Version+json`. The 7.x reference has no `license` field; the 6.20 source (`VersionResource.java`) adds the licence key hash there when a licence is installed, **[I]** for 7.x; no client parses it |
| GET | `/artifactory/api/system` | admin | 200 | `text/plain` system info; currently 501, P38 |
| POST | `/artifactory/api/system/verifyconnection` | admin | 200 | `{endpoint,username,password}`; currently 501, P38 |
| GET | `/artifactory/api/system/license` | admin | 200 | `{"type":"...","validThrough":"...","licensedTo":"..."}`; read only by `platform_license`. Current shim reports `type`/`licensedTo` `artifactweave`, `validThrough` `never`; it asserts no JFrog entitlement |
| POST | `/artifactory/api/system/license` | admin | 200 | `{"licenseKey":"..."}`; currently 501. P40 owns external entitlement integration, not simulated licence installation |
| GET / POST / DELETE | `/artifactory/api/system/licenses[?licenseHash=a,b]` | admin or `system:info/licenses:r` | 200 | HA list `[{type,validThrough,licensedTo,licenseHash,nodeId,nodeUrl,expired}]`. JFrog documents an array body for `POST`, but `platform_license` installs by posting one `{"licenseKey":"..."}` object here and then reads `/license` back ([`resource_license.go`](https://github.com/jfrog/terraform-provider-platform/blob/1dd3a35/pkg/platform/resource_license.go)) |
| GET | `/artifactory/api/system/service_id` | admin | 200 | `text/plain` `jfrt@<uuid>`, the instance id, with no trailing newline; see below |
| GET | `/artifactory/api/storageinfo` | admin or `system:info/storage:r` | 200 | shape in [02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md) |
| POST | `/artifactory/api/storageinfo/calculate` | admin | 202 | |
| POST | `/artifactory/api/system/storage/gc` | admin | 200 | |
| GET | `/artifactory/api/tasks` | admin | 200 | background tasks; currently 501, P38 |
| GET | `/artifactory/api/v1/metrics` | admin | 200 | OpenMetrics text; self-managed only in Artifactory; currently 501. P18 baseline, P38 operational extensions |
| GET | `/artifactory/api/v1/metrics/application` | admin | 200 | Cloud only in Artifactory; currently 501, P38 |
| GET | `/artifactory/api/system/configuration` | admin | 200 | `application/xml`, current delivered descriptor sections (P12 CFG-01) |
| POST | `/artifactory/api/system/configuration` | admin | 200 | full XML replace; currently 501, P36 |
| PATCH | `/artifactory/api/system/configuration` | admin | 200 | `application/yaml`, partial; `null` deletes an element; an empty document deletes every repository |
| PUT | `/artifactory/api/system/configuration/baseUrl` | admin | 200 | `text/plain` URL; currently 501, P12 |
| GET / POST | `/artifactory/api/system/configuration/webServer` | GET: any signed-in user; POST: admin | 200 | reverse proxy settings, shape below. "Non-admin users will get the proxy method and ports and the list of repositories based on their user permissions" ([Get Reverse Proxy Configuration](https://docs.jfrog.com/administration/reference/getreverseproxyconfiguration.md)); the schema has no field for the list, so its name is **[I]** |
| GET | `/artifactory/api/system/configuration/reverseProxy/nginx` | admin | 200 | `text/plain` snippet; currently 501, P12 |
| POST / DELETE / GET | `/artifactory/api/system/debug/loggers[/{class}]` | admin | 200 | `{"classes":{"<logger>":"debug"},"timer":<int>}`, 1–100 loggers; implemented P12 process-local controls; P38 owns operational extensions |
| GET | `/artifactory/api/system/locks[?category=]`, `/locks/local` | admin | 200 | PostgreSQL advisory locks; currently 501, P38 with P35 HA state |
| POST | `/artifactory/api/system/migration/sha2/start`, `/stop` | admin | 200 | currently 501, P36 |
| POST | `/artifactory/api/metadata_server/reindex?async&delete_first` | admin | 200 | `{"paths":[]}`; currently 501, P38. Implemented package-format reindex routes are separate |
| GET / PUT | `/artifactory/api/curation/interfaceSettings` | admin | 200 | Curation add-on; currently 501, P27 |
| POST | `/artifactory/api/system/support/bundle` (+ list, get, delete) | admin | 200 | currently 501, P38 |
| GET | `/access/api/v1/system/ping` | anonymous | 200 | `OK`; a failure status and body are **[I]** |
| GET | `/access/api/v1/system/readiness`, `/access/api/v1/system/liveness` | anonymous | 200 | polled through the public URL by JFrog's tools: the secret rotator fails a rotation unless readiness answers 200 ([jfrog-secret-rotator-lambda `lambda_function.py`](https://github.com/jfrog/jfrog-secret-rotator-lambda/blob/main/secret-rotator/lambda_function.py), lines 159-165), and the Artifactory chart probes it ([jfrog/charts `stable/artifactory/values.yaml`](https://github.com/jfrog/charts/blob/master/stable/artifactory/values.yaml)). No page documents them; the bodies are **[I]** |
| GET | `/access/api/v1/system/version` | anonymous | 200 | `{"name":"<version>","version":"<version>"}`; the `jfrog/platform` provider parses `name` as the Access version |
| GET | `/router/api/v1/system/health` | anonymous | 200 / 503 | `{"router":{"node_id","state","message"},"services":[{"service_id":"jfrt@...","node_id","state","message"}]}`; 503 when a service is unhealthy, with a state such as `UNHEALTHY` or `UNHEALTHY_PEER` and the failure in `message`. The reference page `https://docs.jfrog.com/administration/reference/routerhealthcheck` now answers 404 and `https://jfrog.com/help/r/jfrog-rest-apis/health-check` redirects to Mission Control's `mcsystemping`; the fields and the 503 come from the search-indexed copy of the removed page, which "returns health state and general information regarding the router and locally registered services". A **[KB]** [article](https://jfrog.com/help/r/artifactory-how-to-use-artifactory-health-check/sample-response-for-health-check-on-artifactory-ha) shows ids such as `jfrt@01d8m3sm6q0rj21rqj47331ayc` and `jfac@01cbdrkbfvv5t20g5ke9rk0339`, read from the router's `topology/health`. Which entry carries which unhealthy state, and the exact messages, are **[I]**. JFrog's clients read only the status (jfrog/charts `migrationStatus.sh`, the Ansible roles) |
| GET | `/router/api/v1/system/readiness`, `/router/api/v1/system/liveness` | anonymous | 200 | polled through the public URL: Wingman "polls `<jfrogUrl>/router/api/v1/system/readiness` before startup" (jfrog/charts `stable/wingman/CHANGELOG.md`). No page documents them; the bodies are **[I]** |
| GET | `/router/api/v1/system/ping` | anonymous | 200 | only in the third-party mirror; no 7.x page documents it: **[I]**, unconfirmed for 7.x |
| POST | `/artifactory/api/system/usage` | any **[I]** | 200 / 202 | undocumented usage ping sent by the Terraform providers at configure time and by `jf` on servers ≥6.9.0; accepted and ignored up to 1 MiB, 413 above it ([0016](decisions/0016-observability-and-health.md#decision)). The open [RTFACT-22629](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-22629) lists it as "Auth required" |
| POST | `/jfconnect/api/v1/backoffice/metrics/log` | any | 200 / 201 | sent by every `jf` command when the server reports ≥7.102; failures are only debug-logged; accepted and ignored up to 1 MiB, 413 above it |
| GET / POST | `/artifactory/api/import/system` | admin | 200 | `{importPath,includeMetadata,verbose,failOnError,failIfEmpty}`; currently 501, P36 |
| POST | `/artifactory/api/import/repositories?path&repo&metadata&verbose` | admin | 200 | currently 501, P36 |
| GET / POST | `/artifactory/api/export/system` | admin | 200 | `{exportPath,includeMetadata,createArchive,bypassFiltering,verbose,failOnError,failIfEmpty,m2,incremental,excludeContent}`; currently 501, P36 |
| POST | `/artifactory/api/artifactexport/repository` | admin | 200 | `{action:"repository",path,repository,excludeMetadata,m2,verbose}`; currently 501, P36 |

How artifactweave answers the probes ([decision 0016](decisions/0016-observability-and-health.md#decision)):
each service's probes check what that service needs. Artifactory's ping and readiness, and
the router's readiness, check the database and the binary store; Access's ping and
readiness check the database. A probe reads the instance identity and asks the binary store
for one blob, each within 5 seconds, and a failure answers 500 with a reason that names the
part that failed and nothing else. The liveness probes check no store. Router health names
the services `jfrt@<uuid>` and `jfac@<uuid>` by the stored instance id, gives every entry
the host name as its `node_id`, marks a failing service `UNHEALTHY` with the reason as its
message and the router `UNHEALTHY_PEER`, and then answers 503. `GET .../webServer` gives a
signed-in user who is not an administrator `dockerReverseProxyMethod` and the port fields,
and nothing else. Writing `webServer` is currently `501` and remains P12. HA licence
list/install/delete routes are also `501`; their external entitlement contract belongs
to P40. The single licence GET shim does not implement them.

`service_id` returns the instance id, `jfrt@<uuid>`, as `text/plain` to administrators
([Get Service ID](https://docs.jfrog.com/administration/reference/getserviceid-1.md):
"Security: Requires an admin user", example `jfrt@ee27b1d1-534d-4723-80b5-5bd893d19c43`). Two
`jf` commands read it:
- `jf rt atc --grant-admin` (`jf rt access-token-create`) asks the legacy token API for the
  scope `<service id>:admin`, appending `:admin` to the body as read
  ([`createaccesstoken.go`](https://github.com/jfrog/jfrog-cli-artifactory/blob/4a0f3827cfa0a92d7c74c315335b3c8800044b25/artifactory/commands/generic/createaccesstoken.go),
  lines 112-131; [`system.go`](https://github.com/jfrog/jfrog-client-go/blob/3b4b900/artifactory/services/system.go),
  `GetServiceId`), so the body carries no trailing newline.
- `jf rt transfer-files` reads the source instance's
  ([`transfer.go`](https://github.com/jfrog/jfrog-cli-core/blob/25dbeb0335c03f14a284b1a34c654a9ceadf85b5/artifactory/commands/transferfiles/transfer.go),
  line 372).

The top-level `jf atc --grant-admin` does not call it: it asks Access for
`applied-permissions/admin` directly
([`accesstokencreate.go`](https://github.com/jfrog/jfrog-cli-core/blob/25dbeb0335c03f14a284b1a34c654a9ceadf85b5/general/token/accesstokencreate.go),
lines 146-147). artifactweave serves the stored instance id, the one its token issuer
`jfac@<uuid>` is derived from ([decision 0005](decisions/0005-authentication-and-tokens.md)),
so the scope the CLI builds names this instance. Anonymous callers get 401 and other users
403, as for the licence; that anonymous gets 401 while anonymous access is on is **[I]**.

Temporary logger administration (P12, [#1827](https://github.com/weaveplatform/artifactweave/issues/1827))
uses the primary [Set](https://docs.jfrog.com/administration/reference/setloggerdebuglevels),
[Delete](https://docs.jfrog.com/administration/reference/deleteloggerdebuglevels) and
[Get](https://docs.jfrog.com/administration/reference/getloggerdebuglevel) contracts,
rechecked 2026-11-22. Set requires 1–100 classes and resets previous settings after
its timer; changing an active session requires DELETE first. Unknown classes are
accepted without creating a logger, and GET falls back to the root level.
The primary pages do not show exact success-message bodies or the active-POST
refusal status: retaining JSON confirmation strings and 400 for a conflicting POST
are explicit local engineering choices in the existing served spec. The primary
400 description says a timer below zero is invalid; the existing positive-timer
contract is retained to keep every accepted session bounded. Durations that would
overflow Go's timer (more than 9,223,372,036 seconds) are rejected before publication.

artifactweave names its actual structured log producers `artifactweave` (root),
`artifactweave.http` (access and panic records), `artifactweave.access`,
`artifactweave.artifacts`, `artifactweave.configuration` and `artifactweave.work`.
These are local component names, not captured JFrog Java classes. A registered
child inherits a root override unless a more specific registered name is listed;
unknown override keys do not change existing loggers. `trace`/`debug` map to slog
Debug, `fatal`/`error` to Error, with `info` and `warn` unchanged, as the pre-existing
schema states. GET reports that effective slog level as a lowercase JSON string.

Configuration core validates the whole request and owns one session/timer; the
platform logging mechanism changes real handler decisions while retaining the
original structured sink, attributes, groups and redaction. A failed guarded
current-credential/current-admin check or failed authority transaction changes no
runtime setting. DELETE, expiry and module/runtime Close discard the temporary
override and restore the original handler's current baseline, including baseline
changes made during the session. Cancelled old callbacks cannot clear replacement
sessions; Close joins callbacks already running. Overrides are never configuration
documents, are not exported, and do not survive restart. This P12 implementation is
process-local; vendor HA propagation remains P35, while P38 owns live log streaming,
downloads, support bundles and operational collection.

All three logger module API operations require a standalone context. A context
carrying an enclosing transaction on the configuration metadata store is rejected
before entering the logger lock or changing a decision/timer. In particular, a
transaction-scoped Level must not wait behind a mutation whose authority check
needs that same store/administration guard. Volatile changes also cannot
acknowledge the outer transaction's future outcome. Each accepted call finishes
its own current-authority transaction before reading or publishing. Descriptor
transaction composition is unchanged.

A standalone `app.NewRouter` binds an internally owned logger API only when its
caller supplies `OnClose` and runs those registered callbacks after draining the
handler. Without a registrar it creates no internal logger control or session
service; ordinary records can still use an externally supplied control. The
unbound logger endpoints return 501. An injected Loggers API, including a partial configuration API, retains its
external owner. Production passes the runtime-owned API and control; runtime
shutdown performs that module's cleanup. Registered cleanup is safe to repeat.

Reverse proxy (`webServer`) shape:

```json
{"key":"nginx","webServerType":"NGINX","artifactoryAppContext":"artifactory","publicAppContext":"artifactory",
 "serverName":"artifactory.example.com","serverNameExpression":"*.example.com","artifactoryServerName":"localhost",
 "artifactoryPort":8081,"sslCertificate":"","sslKey":"","dockerReverseProxyMethod":"REPOPATHPREFIX",
 "useHttps":true,"useHttp":true,"sslPort":443,"httpsPort":443,"httpPort":80}
```

`webServerType` is `NGINX | APACHE`. `dockerReverseProxyMethod` is documented as
`SUBDOMAIN | REPOPATHPREFIX | PORTPERREPO`; the `platform_reverse_proxy` resource also
sends `DIRECT`. The resource reads and writes `httpsPort` where the docs say `sslPort`, and
never sends `serverNameExpression` or `artifactoryPort`, so both port keys are emitted and
the two unsent fields are optional.

## Version-string inputs

What each client reads, from the provider and CLI sources
(https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/provider/framework.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/util.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/provider.go,
https://github.com/jfrog/jfrog-cli-core/blob/master/common/commands/command.go):

| Client | Calls at configure or start | Parses |
|---|---|---|
| `jfrog/artifactory` provider | `GET /artifactory/api/system/version`, then async `POST /artifactory/api/system/usage` | `version` only |
| `jfrog/platform` provider | the above plus `GET /access/api/v1/system/version` | `version`, and `name` from Access |
| `jfrog/project` provider | `GET /artifactory/api/system/version`, usage ping | `version` |
| `jf` CLI | no startup check; `GET /artifactory/api/system/version` for usage reporting and before uploads; `POST /artifactory/api/system/usage`; `POST /jfconnect/api/v1/backoffice/metrics/log` on ≥7.102 | `version` |

No provider calls `/api/system/license` at configure time. `CheckArtifactoryLicense` exists
in the shared library with no callers, and the `check_license` attribute was removed.

Per-resource gates that the version string unlocks:

| Version | Feature |
|---|---|
| 6.9.0 | CLI usage reporting |
| 7.53.1 | project environments on repositories |
| 7.72.0 | `platform_permission` |
| 7.82.2 | CLI multipart upload for files ≥200 MiB (probes `GET /api/v1/uploads/config`) |
| 7.84.3 | users through Access v2 |
| 7.90.10 | AWS IAM role mapping |
| 7.102 | CLI jfconnect metrics |
| 7.104.0 | Helm enforce layout |
| 7.107.1 | multiple `environments` per repository |
| 7.122.0 | remote Terraform fields |
| 7.128.0 | group roles |
| 7.146.0 | remote generic `customHttpHeaders` |
| Access 7.138 / 7.144 | newer OIDC fields |
| Access 7.155.0 | lifecycle stages |

Decision ([ADR 0001](decisions/0001-mirror-the-jfrog-api-surface.md)): report a configurable
version defaulting to 7.155.0 or newer on both endpoints, keep the real artifactweave
version in `revision`, and return an empty `addons` list. Readiness reports health, not a
build version. Multipart upload is implemented: `GET /api/v1/uploads/config` reports
support when its service is configured, with `{"supported":false}` only when it is
unavailable (`internal/artifacts/adapters/http/items.go`, `GetUploadsConfig`). The
earlier unconditional fallback is historical, not the current CLI path.

## The configuration descriptor

No JSON endpoints exist for mail, backups, garbage collection, property sets, repository
layouts or proxies. They are sections of the descriptor, edited either as a YAML patch
(`PATCH /api/system/configuration`, `Content-Type: application/yaml`) or as a full XML
replacement. Sources: https://docs.jfrog.com/installation/docs/artifactory-yaml-file-format.md,
https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd.

### Semantics

- The format is `key: [entity]`; named entities are maps keyed by their name
  (`localRepositories.docker-local`).
- A patch creates what does not exist and updates what does; omitted keys are untouched.
- Deletion is `key: null` (or `~`). Inside a repository element, only string fields other
  than `type` may be set to `null`; other fields take a value of their own type
  ([D] https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md).
- An empty document deletes every local, remote and virtual repository. artifactweave
  rejects an empty patch with 400 instead.
- `security.ldapSettings` (LDAP-01, [#1832](https://github.com/weaveplatform/artifactweave/issues/1832))
  writes through the access module's LDAP service, the one behind
  `/access/api/v1/ldap/settings` ([research 05](05-platform-identity-and-security-apis.md#ldap-connections)),
  so the descriptor and the Access API read back one connection. Each entry is a change
  field by field: an omitted field keeps its stored value (or its default on create), a
  `null` string clears it, an omitted `managerPassword` keeps the sealed one and a `null`
  or `''` one clears it, and an entry set to `null` deletes the connection. A `key` field,
  when present, must equal the entry key. A `null` flag or `search` block, an unknown
  field (including the Access API's snake_case names) and every connection the Access API
  would refuse answer 400 naming `security.ldapSettings.<key>` and the field, before anything
  in the document is written. **[I]** A whole-section `ldapSettings: null` is refused, as
  for `mailServer`; the deprecated `artifactory_ldap_setting` resource deletes with it,
  and `artifactory_ldap_setting_v2` deletes through the Access API instead. Reapplying a
  document changes nothing and keeps each connection's revision. `ldapGroupSettings`
  arrives with LDAP-03.
- A successful patch answers `<n> changes to config merged successfully` ([J]
  [RTFACT-31174](https://jfrog.atlassian.net/browse/RTFACT-31174) quotes "14 changes to
  config merged successfully" from 7.84; [RTFACT-17040](https://jfrog.atlassian.net/browse/RTFACT-17040)).
  The `curl` output RTFACT-31174 quotes ends in `%`, the mark zsh prints for output without
  a final newline.
- `<n>` counts the document, not what it changed. The 6.20 `ConfigResource.patchConfig`
  answers `newDataDiffs.size()`, the size of the set `MapToDiffConverter.mapToListOfChanges`
  builds from the parsed YAML before anything is merged. **[I]** for 7.x, which only the
  6.20 source shows. The converter is in JFrog's `jfrog-annotation-processing` library, whose 7.87.1 source
  JFrog publishes
  ([sources jar](https://releases.jfrog.io/artifactory/jfrog-jars/org/jfrog/common/jfrog-annotation-processing/7.87.1/jfrog-annotation-processing-7.87.1-sources.jar)):
  it makes one change for each scalar and each null, descends into mappings, and descends
  into each element of a list, so an empty mapping or list adds nothing ([S]
  `org/jfrog/common/config/diff/MapToDiffConverter.java`). A patch applied a second time
  gives the same number.
- Artifactory's bootstrap file,
  `$JFROG_HOME/artifactory/var/etc/artifactory/artifactory.config.import.yml`, is not this
  format. It holds `version`, `GeneralConfiguration{licenseKey, baseUrl, proxies[]}` and
  `OnboardingConfiguration{repoTypes[]}`, the package types to create default repositories
  for. Artifactory applies it once at first start, only when no repositories exist, and
  deletes it after loading ([D] https://docs.jfrog.com/installation/docs/artifactory-bootstrap-yaml-file.md).
- artifactweave's `--bootstrap <file>` takes the PATCH format instead, the same document as
  `artifactweave config apply <file>`, and applies it once, on the first start
  (`internal/app/descriptor.go`). A real `artifactory.config.import.yml` is refused, because
  none of its sections is one the PATCH applies, and the start stops.
- The YAML repository keys differ from the JSON ones: `type` for `packageType`,
  `repoLayout` for `repoLayoutRef`, `xray.enabled` for `xrayIndex`, and per-type blocks such
  as `helm.chartsBaseUrl`, `nuget`, `vcs`, `externalDependencies`. [Research
  03](03-repository-configuration-model.md#json-and-yaml-name-differences) lists every
  difference and where artifactweave stores each key.

### Top-level elements (XSD order)

`repositoriesGeneralConfig`, `serverName`, `offlineMode`, `fileUploadMaxSizeMb`,
`dateFormat`, `addons`, `mailServer`, `xrayConfig`, `security`, `backups`, `indexer`,
`localRepositories`, `remoteRepositories`, `virtualRepositories`, `federatedRepositories`,
`releaseBundlesRepositories`, `proxies`, `reverseProxies`, `propertySets`,
`systemProperties`, `urlBase`, `federatedRepoUrlBase`, `footer`, `repoLayouts`,
`remoteReplications`, `localReplications`, `gcConfig`, `cleanupConfig`,
`virtualCacheCleanupConfig`, `quotaConfig`, `folderDownloadConfig`, `trashcanConfig`,
`itemsBrowserPagingSize`, `replicationsConfig`, `sumoLogicConfig`, `releaseBundlesConfig`,
`signedUrlConfig`, `downloadRedirectConfig`, `eula`, `subscription`, `keyPairs`,
`retentionSettings`, `authentication`, `systemMessageConfig`.

### Section shapes

| Section | Keys |
|---|---|
| `urlBase`, `fileUploadMaxSizeMb`, `dateFormat`, `offlineMode`, `serverName` | scalars |
| `proxies.<key>` | `host, port, username, password, domain, ntHost, redirectedToHosts, platformDefault, services[jfrt,jfmc,jfxr,jfds]` |
| `mailServer` | `enabled, artifactoryUrl, from, host, port, username, password, subjectPrefix, ssl, tls` |
| `propertySets.<name>` | `properties{<prop>{predefinedValues, closedPredefinedValues, multipleChoice}}, visible` |
| `backups.<name>` | `cronExp, retentionPeriodHours, createArchive, excludedRepositories, excludeNewRepositories, sendMailOnError, enabled` |
| `indexer` | `enabled, cronExp, includedRepositories` |
| `gcConfig`, `cleanupConfig`, `virtualCacheCleanupConfig` | `cronExp` |
| `quotaConfig` | `enabled, diskSpaceLimitPercentage, diskSpaceWarningPercentage` |
| `trashcanConfig` | `enabled, retentionPeriodDays` |
| `replicationsConfig` | `blockPullReplications, blockPushReplications` |
| `systemMessageConfig` | `enabled, message, title, titleColor` |
| `repoLayouts.<name>` | `artifactPathPattern, distinctiveDescriptorPathPattern, descriptorPathPattern, folderIntegrationRevisionRegExp, fileIntegrationRevisionRegExp` (field names from the XSD; the YAML page does not list them). The `artifactory_repository_layout` resource also sends `name`, repeating the entry key ([S] provider 12.11.14 [`resource_artifactory_repository_layout.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_repository_layout.go)) |
| `security` | `anonAccessEnabled, anonAccessToBuildInfosDisabled (deprecated), userLockPolicy{enabled, loginAttempts}, passwordSettings{encryptionPolicy: REQUIRED\|SUPPORTED\|UNSUPPORTED, expirationPolicy{enabled, passwordMaxAge, notifyByEmail}}, ldapSettings.<key>{key, ldapUrl, emailAttribute, ldapPoisoningProtection, allowUserToAccessProfile, search{managerDn, managerPassword, searchBase, searchFilter, searchSubTree}, userDnPattern, autoCreateUser, enabled, pagingSupportEnabled}, ldapGroupSettings.<name>{enabledLdap, filter, groupBaseDn, groupMemberAttribute, groupNameAttribute, strategy: STATIC\|DYNAMIC\|HIERARCHY, subTree}, crowdSettings{...}, samlSettings{...}, oauthSettings{enableIntegration, persistUsers, allowUserToAccessProfile, oauthProvidersSettings.<name>{providerType: github\|openId\|google\|cloudfoundry, ...}}, httpSsoSettings{httpSsoProxied, remoteUserRequestVariable, allowUserToAccessProfile, noAutoUserCreation, syncLdapGroups}` |
| `localRepositories.<key>` | `type, checksumPolicyType, repoLayout, snapshotVersionBehavior, xray{enabled}, downloadRedirect{enabled}, blackedOut, handleReleases, handleSnapshots, includesPattern, excludesPattern, propertySets, yumGroupFileNames, primaryKeyPairDescriptor, secondaryKeyPairRef, ...`; `type` includes `terraformbe` for Terraform backend repositories |
| `remoteRepositories.<key>` | the local keys plus `url, username, password, proxy, socketTimeoutMillis, assumedOfflinePeriodSecs, retrievalCachePeriodSecs, missedRetrievalCachePeriodSecs, bower, cocoaPods{cocoaPodsSpecsRepoUrl}, nuget, helm, composer, vcs, externalDependencies, contentSynchronisation`; `remoteRepoLayout` in JFrog support's YAML ([research 03](03-repository-configuration-model.md#json-and-yaml-name-differences)) |
| `virtualRepositories.<key>` | `repositories[], includesPattern, excludesPattern, p2{urls[]}, keyPair, virtualCacheConfig{virtualRetrievalCachePeriodSecs}, forceMavenAuthentication` |
| `federatedRepositories.<key>` | local keys plus `federatedMembers[]`, `proxy`, `primaryKeyPairRef` (list format in the docs; echoed but treated as local) |

Proxy `platformDefault`, property-set `visible`, property
`closedPredefinedValues`/`multipleChoice`, and predefined-value `defaultValue`
are booleans; the other proxy fields except numeric `port` are strings ([S]
provider 12.11.14 [proxy model](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_proxy.go)
and [property-set model and transformation](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_property_set.go)).
So `services` and `redirectedToHosts` are comma-separated strings, not lists,
as the XSD's `ProxyType` also declares them `xs:string`.
artifactweave validates these merged documents before publication. Property
definitions accept the three fields above. Named predefined values accept
`{defaultValue: <boolean>}` or null; YAML lists also accept strings and
`{value: <string>, defaultValue: <boolean>}` objects. Unknown object fields and
wrong scalar/container types refuse the entire patch; omitted values, explicit
false and the existing null forms remain accepted.

Example (from https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md):

```yaml
localRepositories:
  docker-local:
    type: docker
    repoLayout: simple-default
    xray:
      enabled: false
remoteRepositories:
  maven-central:
    type: maven
    url: https://repo1.maven.org/maven2/
    retrievalCachePeriodSecs: 7200
virtualRepositories:
  maven-all:
    type: maven
    repositories:
      - maven-local
      - maven-central
proxies:
  corp:
    host: proxy.example.com
    port: 3128
    platformDefault: true
    services: jfrt,jfxr
security:
  anonAccessEnabled: true
  userLockPolicy:
    enabled: false
    loginAttempts: 5
```

Proxy `domain` and `ntHost` are runtime NTLMv2 settings for artifact remote sources
(#1835), using the configured username and sealed proxy password. This follows the
7.161.26 `ProxyDescriptor` / `artifactory.xsd` field meanings: domain/realm and the
computer name of this client. [MS-NTHT](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-ntht/f8064832-931f-4516-9557-01630745732f)
supplies the HTTP proxy handshake, while the [remote transport contract](../implementation/remote-http-transport.md)
distinguishes local pinning, CONNECT and supported NTLMv2 capability restrictions.
The separate replication consumer remains #1848.
No Windows administration or LDAP NTLM login is implied.

### Proxy redirect credential contract

The [official proxy guide](https://jfrog.com/help/r/jfrog-platform-administration-documentation/define-proxies)
and pinned 7.161.26 XSD describe reuse of proxy credentials for listed redirect hosts.
`ProxyDescriptor.getRedirectedToHostsList` and the shared client's `ProxyConfig` split
newline, comma, semicolon and ASCII space. The [12.11.14 provider](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_proxy.go)
serializes its set as a comma-separated string and splits commas on read.
The actual 13.21.0 `ProxyConfig.setRedirectedToHosts` bytecode calls
`StringUtils.trim`; [Apache Commons Lang 3.20.0](https://github.com/apache/commons-lang/blob/rel/commons-lang-3.20.0/src/main/java/org/apache/commons/lang3/StringUtils.java#L8722)
delegates to `String.trim`, removing only edge characters through U+0020.
artifactweave preserves that boundary: Unicode edge whitespace is invalid, while
ASCII edge controls are trimmed and embedded tabs/CR remain invalid host tokens.

The [7.161.26 sources](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
name `artifactory-service-core-parent:7.161.15`, whose [public POM](https://releases.jfrog.io/artifactory/jfrog-jars/org/artifactory/artifactory-service-core-parent/7.161.15/artifactory-service-core-parent-7.161.15.pom)
inherits [artifactory-parent-pom:7.161.15](https://releases.jfrog.io/artifactory/jfrog-jars/org/artifactory/artifactory-parent-pom/7.161.15/artifactory-parent-pom-7.161.15.pom).
That parent sets `jfrog.commons.version=13.21.0`. `HttpRepo.createHttpClient` calls
`HttpClientConfigurator.proxy(ProxyDescriptor)`, which delegates to inherited
`HttpBuilderBase.proxy(ProxyConfig)`. The official
[jfrog-http-client:13.21.0 JAR](https://releases.jfrog.io/artifactory/jfrog-jars/org/jfrog/common/jfrog-http-client/13.21.0/jfrog-http-client-13.21.0.jar)
(SHA-256 `434cd89d56efc5b6e78c6ba7b5d45aa612db550f4b1266902908fb4c10b30511`)
has no published sources classifier. Its `javap -c -p` output directly establishes that
`ProxyConfigBuilder.redirectToHostProxies` registers each host with **proxyPort** and
`ANY_REALM` in the credentials provider. The older [7.86.2 source archive](https://releases.jfrog.io/artifactory/jfrog-jars/org/jfrog/common/jfrog-http-client/7.86.2/jfrog-http-client-7.86.2-sources.jar)
has the same method sequence. The deprecated static server `configureProxy` uses
`ANY_PORT`; using that helper to define the ordinary remote's behavior would select
the wrong call path.

The shared provider supplies credentials to target as well as proxy authentication.
The JAR's `PreemptiveAuthInterceptor` looks up target hostname/port. Apache's primary
[TargetAuthenticationStrategy](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/impl/client/TargetAuthenticationStrategy.java)
uses 401/WWW-Authenticate; its [MainClientExec](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/impl/execchain/MainClientExec.java)
keeps target Authorization and proxy Proxy-Authorization separate. These are source and
bytecode facts, not observed vendor responses or a claim about every configured scheme.
Apache's [AuthScope](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/auth/AuthScope.java)
lowercases host keys and compares their strings literally; [HttpHost](https://github.com/apache/httpcomponents-core/blob/4.4.x/httpcore/src/main/java/org/apache/http/HttpHost.java)
preserves the hostname. Credential matching therefore preserves terminal dots and IP
text rather than adding DNS/IP aliases. The local URL syntax contract uses Go's
`URL.Hostname`: IPv6 brackets delimit the authority and are removed for matching;
configured host tokens remain unbracketed IP literals.
DNS label validation follows full root-locale lowercasing. OpenJDK's
[ConditionalSpecialCasing](https://github.com/openjdk/jdk21u/blob/master/src/java.base/share/classes/java/lang/ConditionalSpecialCasing.java#L61)
expands `İ` into `i` plus U+0307; Go's simple `strings.ToLower` would incorrectly
grant ASCII `i`. The existing pinned
[`x/text` Lower](https://github.com/golang/text/blob/v0.44.0/cases/cases.go)
with `language.Und` preserves that expansion and Kelvin-to-`k`. Each invocation owns
its caser because the API permits stateful instances. Unicode 15/17
[`UnicodeData`](https://www.unicode.org/Public/17.0.0/ucd/UnicodeData.txt) and
[`SpecialCasing`](https://www.unicode.org/Public/17.0.0/ucd/SpecialCasing.txt)
have the same ASCII-label boundary: Kelvin is the only non-ASCII rune whose full root
lowercase becomes an ASCII label. Root-context sigma stays non-ASCII. The exhaustive
mapping regression preserves this boundary without Unicode case folding or IDNA conversion.

P12 #1836 implements a single Basic target-challenge retry at that exact host/port,
including permitted HTTP-to-HTTP redirects. **Local choices [I]:** actual redirects
only, bodyless GET/HEAD only, strict host validation without wildcards/URLs/port
tokens, configured repository Authorization precedence and `NoCrossOriginAuth`
restriction. No credential rule adds reachability or forwards repository headers,
TLS identity or cookies beyond their independent rules. DNS is checked and pinned for
both attempts; HTTPS downgrade remains refused. The sealed proxy document/secret
already participates in the source HMAC and transactional publication fence.
Controlled native and SQLite/PostgreSQL proofs cover content and failed publication.
With an NTLM `domain`, `HttpBuilderBase.proxy` registers `NTCredentials` (7.86.2 source;
the 13.21.0 class retains the `NTCredentials`/`redirectToHostProxies` references) for
the same listed hosts, so Apache's `BasicScheme` sends the
[NTUserPrincipal](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/auth/NTUserPrincipal.java)
name: root-locale upper-case domain, a backslash and the username. artifactweave sends that
name in the redirected Basic retry. P19/#216 retains live comparison; #1848 owns broader
replication proxy configuration, preserving its independent endpoint and replay restrictions.

### What artifactweave serves

- `PATCH` applies `localRepositories`, `remoteRepositories`, `virtualRepositories`,
  `federatedRepositories`, `proxies`, `propertySets`, `repoLayouts`,
  `downloadRedirectConfig`, `mailServer`, the `security.anonAccessEnabled` setting and
  the `security.ldapSettings` connections (`internal/configuration/core/descriptor`). Download redirect configuration applies
  the whole-MB `fileMinimumSize` setting used by CDN/storage redirects
  ([research 19](19-cdn-and-download-redirect.md#5-artifactweave-today)). Artifactory
  applies every section its YAML pages document ([D]
  https://docs.jfrog.com/installation/docs/artifactory-yaml-configuration.md). A document
  with any other top-level section answers 400 naming each such section and listing the
  supported sections, and changes nothing. The check runs before `${NAME}` references are resolved, so the
  error names the section rather than a variable inside it. The other sections arrive with
  their features: `backups`, `gcConfig`, `trashcanConfig`, `quotaConfig`,
  `systemMessageConfig` and the remaining baseline security settings in P12
  ([plan](../implementation/plan.md)). Stored federated repository configuration does
  not establish P13 peer synchronization. P36 completes scheduled backup and migration workflows;
  human identity settings follow P21/P22, and product settings follow their owners in the
  [capability roadmap](../implementation/capability-roadmap.md). P20 inventories the
  descriptor/XSD fields individually so a named section cannot conceal an unowned gap.
  A 200 for an unapplied section would tell a pipeline that configuration
  is in force when it is not. Unsupported contents are not stored, including any secrets.
  PRJ-04 supplies the narrow documented singleton `mailServer` fields needed for quota
  delivery. Its password is separated into the existing sealed secret store; omitted
  fields are retained and null clears strings. Whole-section null is refused; disable
  notifications with `enabled: false`. Delivery reads settings and credentials under one
  guarded local transaction before network I/O. CFG-01 exports the public mail fields;
  mail testing endpoints and provider-wide qualification retain their P12 owners.
- A successful patch answers `<n> changes to config merged successfully` as `text/plain`,
  with no newline after it, `<n>` counted as "Semantics" describes ([J] RTFACT-31174,
  RTFACT-17040; [S] `MapToDiffConverter`). `artifactweave config apply` prints its own
  summary instead, naming the entries created, updated, deleted and unchanged.
- Repository entries accept every key in JFrog's YAML samples ([D] [YAML
  page](https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md))
  and `remoteRepoLayout` ([J] RTFACT-29540, RTFACT-29858), alongside the JSON API's names.
  [Research 03](03-repository-configuration-model.md#json-and-yaml-name-differences) says
  where each is stored.
- Inside a repository entry, `null` clears a string field. On `type`, on a block such as
  `xray`, and on any field that does not hold a string it answers 400 naming the field
  ([D] [delete an element](https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md)).
  A field with a fixed set of values, such as `checksumPolicyType` or `rclass`, counts as
  a string. **[I]** The page names `type`, whose values are fixed too, as the one exception,
  which reads as counting the others as strings; no source shows 7.x with `null` on one.
- A `repoLayouts` entry may repeat its key as `name`, as the `artifactory_repository_layout`
  resource does in every create and update body ([S] provider 12.11.14, "Section shapes").
  A `name` equal to the key, or `null`, is accepted and not stored, because the key is the
  layout's name. A different `name` answers 400 naming the entry. **[I]** What 7.x does with
  a `name` that differs from the key: no source shows it, and refusing it keeps the key as
  the one identity of a layout.
- `GET` returns an XML descriptor generated from current state, so that the
  `artifactory_*` configuration resources can read back what they wrote. CFG-01 serves
  the currently delivered sections as `application/xml`, with the XSD names and nesting,
  including explicit false/zero/empty values. The `artifactory_repository_layout` resource finds its layout
  by the `name` element of `repoLayouts/repoLayout`, both to read it and before deleting it
  ([S] provider 12.11.14
  [`resource_artifactory_repository_layout.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_repository_layout.go)),
  so the XML gives each layout its key as `name`.
- The export's `security` element carries `ldapSettings/ldapSetting` in XSD order
  (`key, enabled, ldapUrl, userDnPattern, search{searchFilter, searchBase, searchSubTree,
  managerDn}, autoCreateUser, emailAttribute, ldapPoisoningProtection,
  allowUserToAccessProfile, pagingSupportEnabled`), leaving out empty strings and always
  leaving out `managerPassword`.
- CFG-01 collects repositories, proxies, property sets, layouts, anonymous access, mail
  and download redirect configuration in one metadata transaction under the shared
  `Projects.Guard`, before returning bytes after commit. Export refuses an existing
  transaction on that store before any read; section readers join the transaction
  export creates, while descriptor Apply keeps its nested atomic semantics.
  Current administrator authority
  and token ceilings are checked under that guard. The `configuration/api.Export`
  contract and the ordered core document define the extension seam for subsequent
  sections; section readers join the same transaction and perform no network I/O.
- The XML projection follows `base/config/src/main/resources/artifactory.xsd` and the
  JAXB descriptors from the [official 7.161.26 source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
  (SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`).
  This version uses namespace `http://artifactory.jfrog.org/xsd/3.5.11`. JFrog's
  [online XSD page](https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd)
  currently publishes an older namespace; its claim to be the latest does not replace
  the pinned archive evidence. Repository-only REST fields without an XML descriptor
  representation are omitted; this export does not claim new runtime capabilities.
  The CFG-01 review inventories all 141 JSON fields of the current repository model:
  111 have class-specific XML projections and 30 are class identity, API-only bindings
  or trust settings, legacy type-selection flags, credentials or JAXB-transient fields.
  `HttpRepoDescriptor.retrieveSha256FromServer` and remote
  `HuggingFaceMLRemoteConfiguration.mlRepoLayout` are visible even though the XSD lacks
  them; the local `mlRepoLayout` field is transient. The JAXB enum annotations also
  matter: XML uses `generic`, `yum` and `helmOci`, and lowercase VCS type/provider values,
  while the shared remote JSON endpoint keeps its distinct Jackson enum spellings.
  Checksum, snapshot, POM cleanup, Docker API and Terraform enum values already match
  their stored representations. This audit preserves API-only and transient exclusions.
- **Local engineering choice:** credentials are omitted from XML. Artifactory's encrypted
  password tokens are vendor-specific; returning sealed storage bytes or fabricated
  ciphertext would not be a portable credential. The pinned proxy reader copies the
  planned password, and the mail reader leaves its password unchanged ([S] provider
  12.11.14 [proxy](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_proxy.go),
  [mail](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_mail_server.go)).
  Sensitive custom HTTP headers retain their names and sensitivity without their values.
  Secret columns are excluded from the XML projection. Secrets inside a descriptor may be given
  as `${ENV_VAR}` references, resolved at apply time and never written back.
- The security descriptor (`GET/POST /api/system/security`, the security configuration as
  raw XML) is not served. JFrog deprecated it as of 1 July 2026 in favour of the security
  REST APIs and the Access bootstrap YAML ([7.146.7 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)).
  This is the narrow supersession in the
  [roadmap register](../implementation/capability-roadmap.md#supersession-register):
  Security REST remains served or owned by its feature phase, and P36 owns Access
  bootstrap migration. General system XML `POST` remains in scope in P36.
- `GET /artifactory/api/system/locks` and `/locks/local`, new in 7.161.15 for PostgreSQL
  with native locks ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)), currently answer 501. P38 owns their inspection API, using the coordination model qualified in P35.

Primary URLs: https://docs.jfrog.com/administration/reference/jfrog-platform-artifactory-system-api/llms.txt,
https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt,
https://docs.jfrog.com/administration/reference/jfrog-artifactory-import-export-api/llms.txt,
https://docs.jfrog.com/administration/reference/jfrog-artifactory-loggers-api/llms.txt,
https://docs.jfrog.com/installation/docs/artifactory-yaml-configuration.md,
https://docs.jfrog.com/installation/docs/artifactory-yaml-file-format.md,
https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/security-configurations-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/general-general-property-sets-proxy-mail-configurations-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/service-configurations-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/advanced-configurations-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md,
https://docs.jfrog.com/installation/docs/artifactory-bootstrap-yaml-file.md,
https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd.
