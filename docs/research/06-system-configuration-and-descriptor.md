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
| GET | `/artifactory/api/system/configuration` | admin | 200 | `application/xml`, the full descriptor; currently 501, P12 |
| POST | `/artifactory/api/system/configuration` | admin | 200 | full XML replace; currently 501, P36 |
| PATCH | `/artifactory/api/system/configuration` | admin | 200 | `application/yaml`, partial; `null` deletes an element; an empty document deletes every repository |
| PUT | `/artifactory/api/system/configuration/baseUrl` | admin | 200 | `text/plain` URL; currently 501, P12 |
| GET / POST | `/artifactory/api/system/configuration/webServer` | GET: any signed-in user; POST: admin | 200 | reverse proxy settings, shape below. "Non-admin users will get the proxy method and ports and the list of repositories based on their user permissions" ([Get Reverse Proxy Configuration](https://docs.jfrog.com/administration/reference/getreverseproxyconfiguration.md)); the schema has no field for the list, so its name is **[I]** |
| GET | `/artifactory/api/system/configuration/reverseProxy/nginx` | admin | 200 | `text/plain` snippet; currently 501, P12 |
| POST / DELETE / GET | `/artifactory/api/system/debug/loggers[/{class}]` | admin | 200 | `{"classes":{"<logger>":"debug"},"timer":<int>}`, at most 100 loggers; currently 501, P12 with P38 operational integration |
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
unavailable (`internal/server/items.go`, `GetUploadsConfig`). The earlier unconditional fallback is
historical, not the current CLI path.

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
| `security` | `anonAccessEnabled, anonAccessToBuildInfosDisabled (deprecated), userLockPolicy{enabled, loginAttempts}, passwordSettings{encryptionPolicy: REQUIRED\|SUPPORTED\|UNSUPPORTED, expirationPolicy{enabled, passwordMaxAge, notifyByEmail}}, ldapSettings.<key>{ldapUrl, emailAttribute, ldapPoisoningProtection, search{managerDn, managerPassword, searchBase, searchFilter, searchSubTree}, userDnPattern, autoCreateUser, enabled, pagingSupportEnabled}, ldapGroupSettings.<name>{enabledLdap, filter, groupBaseDn, groupMemberAttribute, groupNameAttribute, strategy: STATIC\|DYNAMIC\|HIERARCHY, subTree}, crowdSettings{...}, samlSettings{...}, oauthSettings{enableIntegration, persistUsers, allowUserToAccessProfile, oauthProvidersSettings.<name>{providerType: github\|openId\|google\|cloudfoundry, ...}}, httpSsoSettings{httpSsoProxied, remoteUserRequestVariable, allowUserToAccessProfile, noAutoUserCreation, syncLdapGroups}` |
| `localRepositories.<key>` | `type, checksumPolicyType, repoLayout, snapshotVersionBehavior, xray{enabled}, downloadRedirect{enabled}, blackedOut, handleReleases, handleSnapshots, includesPattern, excludesPattern, propertySets, yumGroupFileNames, primaryKeyPairDescriptor, secondaryKeyPairRef, ...`; `type` includes `terraformbe` for Terraform backend repositories |
| `remoteRepositories.<key>` | the local keys plus `url, username, password, proxy, socketTimeoutMillis, assumedOfflinePeriodSecs, retrievalCachePeriodSecs, missedRetrievalCachePeriodSecs, bower, cocoaPods{cocoaPodsSpecsRepoUrl}, nuget, helm, composer, vcs, externalDependencies, contentSynchronisation`; `remoteRepoLayout` in JFrog support's YAML ([research 03](03-repository-configuration-model.md#json-and-yaml-name-differences)) |
| `virtualRepositories.<key>` | `repositories[], includesPattern, excludesPattern, p2{urls[]}, keyPair, virtualCacheConfig{virtualRetrievalCachePeriodSecs}, forceMavenAuthentication` |
| `federatedRepositories.<key>` | local keys plus `federatedMembers[]`, `proxy`, `primaryKeyPairRef` (list format in the docs; echoed but treated as local) |

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
security:
  anonAccessEnabled: true
  userLockPolicy:
    enabled: false
    loginAttempts: 5
```

### What artifactweave serves

- `PATCH` applies `localRepositories`, `remoteRepositories`, `virtualRepositories`,
  `proxies`, `propertySets`, `repoLayouts` and `downloadRedirectConfig`
  (`internal/descriptor/parse.go`, `redirect.go`). The last applies the whole-MB
  `fileMinimumSize` setting used by CDN/storage redirects
  ([research 19](19-cdn-and-download-redirect.md#5-artifactweave-today)). Artifactory
  applies every section its YAML pages document ([D]
  https://docs.jfrog.com/installation/docs/artifactory-yaml-configuration.md). A document
  with any other top-level section answers 400 naming each such section and listing the
  seven, and changes nothing. The check runs before `${NAME}` references are resolved, so the
  error names the section rather than a variable inside it. The other sections arrive with
  their features: `backups`, `mailServer`, `gcConfig`, `trashcanConfig`, `quotaConfig`,
  `systemMessageConfig` and `security` in P12 ([plan](../implementation/plan.md)), and
  `federatedRepositories` in P13. P36 completes scheduled backup and migration workflows;
  human identity settings follow P21/P22, and product settings follow their owners in the
  [capability roadmap](../implementation/capability-roadmap.md). P20 inventories the
  descriptor/XSD fields individually so a named section cannot conceal an unowned gap.
  A 200 for an unapplied section would tell a pipeline that configuration
  is in force when it is not, and their contents are not stored meanwhile because
  `mailServer` and `security` carry passwords. No acceptance scenario, reference deployment
  or client flow served so far sends one of them: the `artifactory_*` resources that write
  them also read the XML `GET`, which P12 brings.
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
  `artifactory_*` configuration resources can read back what they wrote. It lands in P12
  and answers 501 until then. The `artifactory_repository_layout` resource finds its layout
  by the `name` element of `repoLayouts/repoLayout`, both to read it and before deleting it
  ([S] provider 12.11.14
  [`resource_artifactory_repository_layout.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/configuration/resource_artifactory_repository_layout.go)),
  so the XML gives each layout its key as `name`.
- Secrets inside a descriptor may be given as `${ENV_VAR}` references, resolved at apply
  time and never written back.
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
