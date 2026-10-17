# Artifactory core REST API inventory

Checked against: Artifactory 7.161.32

Research baseline: 2026-10-12. This document inventories the Artifactory REST API families
this project implements: repositories, artifacts and storage, searches and AQL,
replication, the trash can, and the package-type endpoints for the eight target
ecosystems. Every page in the seven `artifactory/reference/*` indexes was read and its
embedded OpenAPI definition parsed; the status-code column is the oracle for the
table-driven compatibility tests, because the JFrog CLI and Terraform providers check
exact codes (see [09-jfrog-cli-compatibility.md](09-jfrog-cli-compatibility.md)). The
repository configuration body is in [03-repository-configuration-model.md](03-repository-configuration-model.md);
the identity, system and platform families are in
[05-platform-identity-and-security-apis.md](05-platform-identity-and-security-apis.md).

Conventions: paths are relative to `https://{host}/artifactory`; "admin" means a platform
administrator; "Pro" marks features JFrog gates behind a paid tier, all of which are in
scope here. Sources per family are listed at the end.

## Repositories API

Source: <https://docs.jfrog.com/artifactory/reference/repositories-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| PUT | `/api/repositories/{repoKey}` | Create repository | 200 text/plain; 400 key exists or invalid | Admin. Body `application/json` or `application/vnd.org.jfrog.artifactory.repositories.{Local,Remote,Virtual,Federated}RepositoryConfiguration+json`. Missing fields take defaults. Federated needs Enterprise X or Enterprise+. |
| PUT | `/api/v2/repositories/batch` | Create several repositories | 201 text/plain; 400 any invalid; 409 key conflict | Admin. Array of configurations of mixed classes; the whole batch fails if one is invalid. 7.84.3+. |
| POST | `/api/repositories/{repoKey}` | Update repository | 200 text/plain | Admin. Partial update, same media types. |
| POST | `/api/v2/repositories/batch` | Update several repositories | 200 | Admin. 7.104.2+. |
| GET | `/api/repositories/existence` | Check existence | 200 | Admin. `type` (repeatable), `projectKey`. Body `{"exists":bool,"matchingRepoTypes":["LOCAL"],"projectKey":"..."}`. 7.103+. |
| GET | `/api/repositories/{repoKey}` | Get configuration | 200; 404 | Admin or scope `repo:<key>:r`; non-admins receive a partial configuration. |
| GET | `/api/v2/repositories/{repoKey}` | Get configuration v2 | 200; 404 | Returns only the fields relevant to the package type. |
| GET | `/api/v2/repositories/batch?names=a,b` | Get several configurations | 200 | Default batch size 100. 7.84.3+. |
| GET | `/api/repositories/configurations?packageType=&repoType=` | Get all configurations | 200 | Admin. `{"LOCAL":[],"REMOTE":[],"VIRTUAL":[],"FEDERATED":[],"RELEASE_BUNDLE":[]}`; media type `...repositories.RepositoryConfigurationsList+json`. 7.61.3+. |
| GET | `/api/repositories?type=&packageType=&project=` | List repositories | 200 | Admin or scopes `artifact:*:r` / `system:info:r`. `type` is `local`, `remote`, `virtual`, `federated` or `distribution`; `packageType` accepts any value (the documented enum is stale). Body `[{key,type,description,url,packageType}]`; media type `...RepositoryDetailsList+json`. |
| GET | `/api/repositories/{remoteRepoName}/configuration` | Shared remote configuration | 200 | Deprecated; use get configuration. |
| DELETE | `/api/repositories/{repoKey}` | Delete repository | 200; 202 deletion already running; 207 partial, configuration kept; 400 blank, trash or support-bundle key; 404; 409 import in progress or worker veto; 424 federation member removal failed | Admin. Removes configuration and content. The Terraform provider requires exactly 200. The success body is JSON from 7.161.15, plain text before ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)); artifactweave answers JSON. |
| DELETE | `/api/v2/repositories/batch` | Delete several repositories | 200 all deleted; 207 mixed; 400 fail-fast; 403, 409, 424, 500 all failed | Body `["a","b"]`. Response `RepoBulkRemovalReport{reports[],statusMessage}`. New in 7.161.15 ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)). |

## Artifacts and storage API

Source: <https://docs.jfrog.com/artifactory/reference/artifacts-storage-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| GET | `/{repoKey}/{filePath}` | Retrieve artifact | 200; 206 with `Range`; 302 when `downloadRedirect`; 404 | Read permission, anonymous allowed when enabled. Query `skipUpdateStats`, `trace` (text explanation of resolution). Matrix parameters `;key=value` make resolution conditional on properties. Version tokens `[RELEASE]`, `[INTEGRATION]`, `SNAPSHOT` resolve the latest matching version (layout dependent). Response headers `X-Checksum-Md5`, `X-Checksum-Sha1`, `X-Checksum-Sha256`, `Accept-Ranges: bytes`. |
| PUT | `/{repoKey}/{filePath}` | Deploy artifact, create folder | 201 with file info; 400 malformed path; 405 target is virtual without deployment repo; 409 checksum mismatch or duplicate | Deploy permission. Body `application/octet-stream`. Headers `X-Checksum-Sha1`, `X-Checksum-Sha256`, `X-Checksum-Md5` and `X-Checksum` (sha256) are verified. A trailing `/` with no body creates a folder (the CLI expects 201). Matrix parameters set properties. |
| PUT | same with `X-Checksum-Deploy: true` | Deploy by checksum | 201 when a readable blob exists; 404 otherwise | Checksum header is `X-Checksum-Sha1`, `X-Checksum-Sha256` or `X-Checksum` (type by length 32, 40 or 64). On 404 the client uploads normally; the CLI treats any non-2xx the same way. |
| PUT | same with `X-Explode-Archive: true` | Deploy from archive | 201 | zip, tar, tar.gz, tgz. `X-Explode-Archive-Atomic: true` for all-or-nothing. Only the extracted content is stored. Disables checksum deploy in the CLI. |
| GET | `/{repoKey}/{archivePath}!/{entryPath}` | Archive entry download | 200; 404 | Read permission. `!` then `/` is required. |
| DELETE | `/{repoKey}/{filePath}` | Delete item | 204; 404 | Delete permission. Folders delete recursively. Works on remote caches. |
| GET | `/api/storage/{repoKey}/{itemPath}` | Item information | 200; 404 | Without a mode: file or folder info. `?list[&deep=0\|1&depth=N&listFolders=0\|1&mdTimestamps=0\|1&includeRootPath=0\|1]` needs a non-anonymous user. `?stats`. `?lastModified` needs deploy permission and for folders returns the newest item recursively. `?properties[=k1,k2]` (404 when none). `?permissions`. |
| PUT | `/api/storage/{repoKey}/{itemPath}?properties=k1=v1[,v2]\|k2=v3[&recursive=0\|1]` | Set properties | 204 | Local and cache items only. `recursive` defaults to 1. Separate properties with `;` or `\|`, values with `,`; escape `,`, `\`, `\|` and `=` with `%5C`. |
| DELETE | `/api/storage/{repoKey}/{itemPath}?properties=k1,k2[&recursive=0\|1]` | Delete properties | 204; 404 "No properties could be found" (tolerated by the CLI) | `recursive` defaults to 1. |
| PATCH | `/api/metadata/{repoKey}/{itemPath}?recursiveProperties=0\|1&atomicProperties=0\|1` | Update properties | 204; 400 when `props` missing | Annotate permission. Body `{"props":{"k":"v" \| ["v1","v2"] \| null}}`; `null` removes the key. Used by the Terraform `artifactory_item_properties` resource. 6.1.0+. |
| POST | `/api/checksum/sha256` | Set sha256 property | 200 | Admin. Body `{"repoKey","path"}`. Legacy: sets a `sha256` property recursively for pre-5.5 content. |
| POST | `/api/copy/{srcRepo}/{srcPath}?to=/{dstRepo}/{dstPath}[&dry=1][&suppressLayouts=0\|1][&failFast=0\|1]` | Copy item | 200 | Local, remote cache and federated sources. `suppressLayouts` defaults to 1; `dry` to 0. Body `{"messages":[{"level":"INFO\|WARN\|ERROR","message":"..."}]}`, media type `...storage.CopyOrMoveResult+json`. A copy or move keeps the item's creation and last-modified times; 7.125.4 added a feature flag, off by default, that gives the target the operation's time as its creation time ([7.125.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)). artifactweave gives a copy new times ([#349](https://github.com/weaveplatform/artifactweave/issues/349)). |
| POST | `/api/move/{srcRepo}/{srcPath}?to=...` | Move item | 200 | Local repositories only. Same parameters and body as copy. |
| GET | `/api/archive/download/{repoKey}/{path}?archiveType=zip\|tar\|tar.gz\|tgz[&includeChecksumFiles=true]` | Download folder as archive | 200; 403 when folder download is disabled | Requires the "Enable Folder Download" setting; local and cache only; `archiveType` is mandatory. |
| POST | `/api/archive/buildArtifacts` | Download build artifacts as archive | 200 | Build-info feature. |
| GET | `/api/download/{repoKey}/{filePath}?content=none\|progress&mark=N` | Synchronous download with progress marks | 200 | `mark` defaults to 1024. |
| POST | `/api/zap/{repoKey}/{path}` | Zap cache | 200 `{"Message":"Completed zapping cache in path ..."}` | Manage or delete permission on the remote repository. Invalidates cached metadata, not binaries. 7.49.3+. |
| GET | `/api/storageinfo` | Storage summary | 200 | Admin or scope `system:info/storage:r`. |
| POST | `/api/storageinfo/calculate` | Refresh storage summary | 202 | Admin. 6.17.0+. |
| GET | `/api/tasks` | Background tasks | 200 | Admin. |
| POST | `/api/system/storage/optimize` | Optimise storage | 200 | Admin. |
| POST | `/api/system/storage/prune/start`, GET `.../status`, POST `.../stop` | Prune unreferenced data | 200 | Admin. 7.72+. |
| POST | `/api/signed/url`, `/api/signed/url/key` | Signed URLs | 200 | Enterprise. Deferred. |
| POST | `/api/distribute` | Distribute to Bintray | 200 | Deprecated; Bintray closed in 2021. |
| GET | `/api/v1/metrics` | Open Metrics | 200 text | Admin. Self-managed only in JFrog's licensing; served here as Prometheus text. |
| GET | `/api/v1/metrics/application` | Application metrics | 200 | JFrog Cloud only. Not implemented. |

### Response shapes

File info:

```json
{"uri":"https://host/artifactory/api/storage/repo/dir/file.jar",
 "downloadUri":"https://host/artifactory/repo/dir/file.jar",
 "repo":"repo","path":"/dir/file.jar","remoteUrl":"https://upstream/...",
 "created":"2026-10-12T10:00:00.000Z","createdBy":"alice",
 "lastModified":"2026-10-12T10:00:00.000Z","modifiedBy":"alice","lastUpdated":"2026-10-12T10:00:00.000Z",
 "size":"1024","mimeType":"application/java-archive",
 "checksums":{"md5":"...","sha1":"...","sha256":"..."},
 "originalChecksums":{"md5":"...","sha1":"...","sha256":"..."}}
```

`path` carries a leading `/`; `size` is a string; timestamps are ISO 8601 with
milliseconds. Folder info replaces the size and checksum fields with
`"children":[{"uri":"/name","folder":true}]`. The `?list` form is
`{"uri","created","files":[{"uri":"/rel/path","size":"-1" for folders,"lastModified","folder","sha1","mdTimestamps":{}}]}`.
Stats are `{"uri","lastDownloaded":<epoch ms>,"downloadCount","lastDownloadedBy"}`.
Properties are `{"uri","properties":{"k":["v1","v2"]}}` and values are always arrays.
Permissions are `{"uri","principals":{"users":{},"groups":{}}}`.

The storage summary is:

```json
{"binariesSummary":{"binariesCount":"125,726","binariesSize":"3.48 GB","artifactsSize":"4.10 GB","optimization":"84.9%","itemsCount":"...","artifactsCount":"..."},
 "fileStoreSummary":{"storageType":"file-system","storageDirectory":"/data","totalSpace":"...","usedSpace":"...","freeSpace":"..."},
 "repositoriesSummaryList":[{"repoKey":"npm-local","repoType":"LOCAL","foldersCount":1,"filesCount":2,"usedSpace":"1 KB","itemsCount":3,"packageType":"npm","percentage":"0.1%"},
                            {"repoKey":"TOTAL","repoType":"NA","foldersCount":1,"filesCount":2,"usedSpace":"1 KB","itemsCount":3}]}
```

Numbers are human-formatted strings and a `TOTAL` row closes the list.

Checksum sidecars `.sha1`, `.md5`, `.sha256` and `.sha512` are served next to every
artifact and are not items themselves. With `downloadRedirect` enabled a download answers
302 to a signed object-store URL.

## Searches API

Source: <https://docs.jfrog.com/artifactory/reference/searches-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| POST | `/api/search/aql` | AQL | 200; 400 "AQL query is too long" | Authenticated only. Body `text/plain`, at most 6,000 characters (`artifactory.aql.search.query.size.limit`). Header `scan-factor` (7.133+). Response `{"results":[],"range":{"start_pos","end_pos","total","limit"?,"notification"?}}`. Non-admins may query only `items` and must include `repo`, `path` and `name`. |
| GET | `/api/search/artifact?name=&repos=` | Quick search | 200 | Anonymous allowed. Substring of the file name; local and cache only. `{"results":[{"uri"}]}` with storage-API URIs. |
| GET | `/api/search/archive?name=&repos=` | Archive entry search | 200 | Deprecated and scheduled for removal. |
| GET | `/api/search/gavc?g=&a=&v=&c=&repos=&specific=` | GAVC search | 200 | At least one of g, a, v, c. `specific=true` (7.37.9+) returns `{downloadUri,ext,version,classifier}` and includes virtual and remote. |
| GET | `/api/search/prop?<key>=<v1,v2>&repos=` | Property search | 200 | Anonymous allowed. Any query parameter is a property key; a missing value means `*`. |
| GET | `/api/search/checksum?md5=\|sha1=\|sha256=&repos=` | Checksum search | 200 | `{"results":[{"uri"}]}`. |
| GET | `/api/search/badChecksum?type=md5\|sha1&repos=` | Bad checksum search | 200 | Items whose client checksum differs from the computed one; capped at 10,000; `clientMd5` is empty when missing. |
| GET | `/api/search/usage?notUsedSince=<ms>[&createdBefore=<ms>]&repos=` | Not downloaded since | 200 | `{"results":[{"uri","lastDownloaded"}]}`. |
| GET | `/api/search/dates?from=&to=&dateFields=created,lastModified,lastDownloaded&repos=` | Date range search | 200 | Epoch milliseconds or ISO 8601. |
| GET | `/api/search/creation?from=&to=&repos=` | Created in range | 200 | |
| GET | `/api/search/pattern?pattern=repo-key:this/is/*pat*.war` | Pattern search | 200 | Ant pattern without `**`. `{"repositoryUri","sourcePattern","files":[]}`. |
| GET | `/api/search/dependency?sha1=` | Builds for dependency | 200 | Build-info feature. |
| GET | `/api/search/license?unapproved=&unknown=&notfound=&neutral=&approved=&autofind=&repos=` | Licence search | 200 | Deferred. |
| GET | `/api/search/versions?g=&a=[&v=][&remote=1]&repos=` | Artifact versions | 200 | `{"results":[{"version","integration"}]}`; `v` accepts `*` and `?`. |
| GET | `/api/search/latestVersion?g=&a=[&v=][&remote=1]&repos=` | Latest version by layout | 200 text/plain | Returns the version string only; the layout must define `[org]` or `[module]`. |
| GET | `/api/versions/{repo\|_any}/{path\|_any}?listFiles=0\|1&<prop>=<val>` | Latest version by properties | 200 | `{"version":"1.1.2","artifacts":[{"uri"}]}`. Only a lower-case `version` property counts; Maven ordering. |
| POST | `/api/search/buildArtifacts` | Build artifacts search | 200 | Build-info feature. |
| GET | `/api/docker/{repo}/v2/_catalog?n=&last=` | List Docker repositories | 200 | Read permission. `{"repositories":[]}`. |
| GET | `/api/docker/{repo}/v2/{image}/tags/list?n=&last=` | List Docker tags | 200 | `{"name","tags":[]}`. 7.90.1+ filters tags that are not OCI compliant. |
| POST | `/api/docker/{repo}/v2/parentManifests` | Find parent manifest lists | 200 | Body `{"dockerRepository","tag"}`. 7.119+. |

### AQL

Sources: <https://docs.jfrog.com/artifactory/docs/artifactory-query-language.md>,
<https://docs.jfrog.com/artifactory/docs/aql-syntax.md>,
<https://docs.jfrog.com/artifactory/docs/aql-search-criteria.md>,
<https://docs.jfrog.com/artifactory/docs/aql-query-output.md>,
<https://docs.jfrog.com/artifactory/docs/aql-entities-fields-reference.md>.

Grammar:

```text
<domain>.find(<criteria>).include(<fields>).sort({"$asc"|"$desc":[...]}).offset(N).limit(N).distinct(true|false)
items.delete(<criteria>).dryRun("true"|"false")
properties.update(<criteria>).keys("k").newValue("v").dryRun(...)
```

- Primary domains: `items`, `builds` (admin only unless a scoped token grants it),
  `build.promotions`, `releases`, `release_artifacts`, `item.infos`. The `archive.entries`
  domain and all `archive.*` fields have been non-functional since the third quarter of
  2024 and produce a parse error.
- Secondary domains through dotted paths: `property` (also `@key`), `stat`,
  `artifact.module.build`, `module.dependency`, and the release fields.
- Criteria: `{"field":{"$op":value}}`; the shorthand `{"field":value}` means `$eq`;
  property criteria `{"@key":{"$op":"v"}}`, `{"@*":"v"}` for any key and `"*"` for any
  value; `{"$and":[...]}` and `{"$or":[...]}`, with an implicit `$and` between siblings;
  `{"$msp":[...]}` matches on a single property. Operators `$eq`, `$ne`, `$gt`, `$gte`,
  `$lt`, `$lte`, `$match`, `$nmatch` (wildcards `*` and `?` only with `$match` and
  `$nmatch`, matched case-sensitively), and the relative time operators `$before` and
  `$last` with units `ms`, `s`, `minutes`, `d`, `w`, `mo`, `y`. Dates may be partial
  W3C ISO 8601. A zero `stat.downloads` is `null`.
- `$rf` is not generated by the JFrog CLI and is absent from every AQL page; its only
  sighting is JFrog's UI syntax highlighter. artifactweave rejects it with 400
  ([research 17](17-undocumented-shapes.md)).
- Item fields: `repo`, `path` (`.` for the root, no leading slash), `name`, `created`,
  `modified`, `updated`, `created_by`, `modified_by`, `type` (`file`, `folder`, `any`;
  default `file`), `depth`, `original_md5`, `actual_md5`, `original_sha1`, `actual_sha1`,
  `sha256`, `size`, `virtual_repos`. Property fields `key`, `value`. Stat fields
  `downloaded`, `downloads`, `downloaded_by`, `remote_downloads`, `remote_downloaded`,
  `remote_downloaded_by`, `remote_origin`, `remote_path`. Build fields `url`, `name`,
  `number`, `created`, `created_by`, `modified`, `modified_by`, `started`, `repo`.
- Output: the default item fields are `repo`, `path`, `name`, `type`, `size`, `created`,
  `created_by`, `modified`, `modified_by`, `updated`. `.include("*")`,
  `.include("property.*")`, `.include("@version")` and `.include("stat")` widen it.
  `sort`, `limit` and `offset` work only when `include` names primary-domain fields.
  `distinct` defaults to true. `delete` ignores `sort`, `limit` and `offset`. When the
  server trims a result at its hard limit it adds `range.notification`.

## Replication API

Source: <https://docs.jfrog.com/artifactory/reference/repository-replication-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| GET | `/api/replications/{repoKey}` | Get replication configuration | 200; 404 | Local and remote repositories. |
| PUT | `/api/replications/{repoKey}` | Create replication configuration | 201 | Body `application/vnd.org.jfrog.artifactory.replications.ReplicationConfigRequest+json`, one object or an array. |
| POST | `/api/replications/{repoKey}` | Update replication configuration | 200 | |
| DELETE | `/api/replications/{repoKey}[?url=]` | Delete replication configuration | 200 | |
| GET | `/api/replication/{repoKey}[/{path}]` | Replication status | 200 | |
| POST | `/api/replication/execute/{repoKey}[/{path}][?strategy=]` | Run replication now | 202 | Push for a local repository, pull for a remote cache. |
| PUT, POST | `/api/replications/multiple/{repoKey}` | Multi-push replication | 201, 200 | Enterprise. Deferred. |
| POST | `/api/replications/enable`, `/api/replications/disable` | Enable or disable several | 200 | Body with include and exclude URL patterns. |
| GET | `/api/replications/channels/{repoKey}` | Event-pull channels | 200 | |
| POST | `/api/replications/channels/release` | Close event-pull channels | 200 | |
| POST | `/api/replications/channels/establishChannel`, `/removeChannel`, `/api/replications/propagateEvents` | Internal peer protocol | 200 | Not for clients. |
| GET | `/api/system/replications` | Global replication configuration | 200 | |
| POST | `/api/system/replications/block?push=&pull=`, `/api/system/replications/unblock` | Block or unblock | 200 | |
| PUT, GET | `/api/config/storage/checksumReplication` | Checksum replication | 200 | Self-hosted. |

Replication configuration fields: `username`, `password`, `cronExp`, `url`, `repoKey`,
`proxy`, `disableProxy`, `socketTimeoutMillis` (default 15000), `enableEventReplication`,
`enabled`, `syncDeletes`, `syncProperties` (default true), `syncStatistics`, `pathPrefix`,
`includePathPrefixPattern`, `excludePathPrefixPattern`.

## Trash can and garbage collection

Source: <https://docs.jfrog.com/artifactory/reference/trash-cangarbage-collection-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| POST | `/api/trash/empty` | Empty the trash can | 202 | Admin. |
| DELETE | `/api/trash/clean/{repoKey}/{path}` | Delete an item from the trash can | 204 | Project admins or holders of manage or delete permission. |
| POST | `/api/trash/restore/{repoKey}/{path}?to=<repo/path>[&transaction-size=N]` | Restore an item | 202 text/plain | Same permissions as clean. |
| POST | `/api/deleteDirectoryTrashProperty/start` | Remove redundant trash folder records | 200 | JSON body. |
| POST | `/api/system/storage/gc` | Run garbage collection | 200 | Admin. |

## Federated repositories API

Source: <https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt>.

Twenty-five endpoints under `/api/federation/` (migrate, convertToLocal, fullSync,
forceFullSync, configSync, removeMember, status, binaries/failures, prioritization,
state) plus `/api/v1/service_trust/pairing/{usecase}`. All require Enterprise X or
Enterprise+. This project accepts `rclass: federated` only when the sole member is the
instance itself and answers 501 under `/api/federation/`; see
[10-scope-and-tiers.md](10-scope-and-tiers.md).

## Package-type endpoints

Source: <https://docs.jfrog.com/artifactory/reference/package-type-specific-apis/llms.txt>
and <https://docs.jfrog.com/integrations/reference/promotedockerimage.md>. The client
protocols these sit beside are in [04-package-protocols.md](04-package-protocols.md).

| Ecosystem | Method and path | Status | Notes |
|---|---|---|---|
| Docker and OCI | `GET /api/docker/{repo}/v2/_catalog`, `GET /api/docker/{repo}/v2/{image}/tags/list`, and the distribution endpoints `manifests`, `blobs`, `blobs/uploads`, `referrers` under `/api/docker/{repo}/v2/{image}/` | per OCI | Also reachable through the repository-path, subdomain and port access methods. |
| | `GET /api/docker/{repo}/v2/auth` (legacy `/v1/auth`) | 200 | Returns a `config.json` auth entry `{"auth":base64(user:pass),"email"}`. |
| | `POST /api/docker/{repo}/v2/promote` | 200 | Body `{"targetRepo","dockerRepository","targetDockerRepository"?,"tag"?,"targetTag"?,"copy":false}`. Local and federated sources. Also used for Helm OCI. |
| | `DELETE /api/docker/{repo}/v2/delete` | 200 | Body `{"dockerRepository","tag"}`; deletes a multi-architecture tag. 7.91+. |
| | `POST /api/docker/{repo}/v2/parentManifests` | 200 | 7.119+. |
| npm | `GET /api/npm/auth`, `GET /api/npm/{repo}/auth/{scope}` | 200 text | Legacy `.npmrc` helpers *(unverified; absent from current pages)*. |
| | `POST /api/npm/{repo}/reindex` | 200 | Local repositories only. |
| | `POST /api/v1/migrations/npm/repositories/{repo}/migrate`, `.../resume` | 202 | Strict-layout conversion; manage permission. |
| Maven | `POST /api/maven?repos=a,b[&force=1]` | 200 | Calculate Maven index. |
| | `POST /api/maven/calculateMetadata/{repo}/{folder}[?noneRecursive=true]` | 200 | The parameter is spelled `noneRecursive`. Local only. |
| | `POST /api/maven/generatePom/{repo}/{jar}` | 200 | |
| PyPI | `POST /api/pypi/{repo}/reindex[?async=1]` | 200 | Local only. `packages/**` and `simple/**` are reserved paths and writes there answer 400. |
| | `GET /api/pypi/{repo}/pypi/{project}/json`, `.../{project}/{version}/json` | 200 | PyPI JSON API shape. |
| NuGet | `POST /api/nuget/{repo}/reindex` | 200 | Local, cache, remote and virtual. |
| Helm | `POST /api/helm/{repo}/reindex` | 200 | Local and federated. |
| | `POST /api/helm/{repo}/{path}/reindex` | 200 | Partial reindex; the path is a segment, not a query parameter. 7.105.2+. |
| Terraform | `POST /api/terraform/{repo}/reindex` | 200 | |
| Go | none | | There is no reindex endpoint; `@v/list` is derived from stored items. |

The reindex endpoints of other ecosystems (alpine, cocoapods, yum, deb, opkg, bower,
cran, cargo, conda, conan, pub, gems, swift, puppet, vcs) are out of scope.

## Deprecated and licence-gated endpoints

Deprecated by JFrog: `GET /api/repositories/{repo}/configuration`, `GET /api/search/archive`,
the AQL `archive.entries` domain, `POST /api/distribute`, `POST /api/checksum/sha256`
(kept for pre-5.5 content), Docker API `V1` and `/v1/auth`, npm `_auth` basic
authentication, the nuget.org V2 OData feed, and the `[Internal]` replication channel
endpoints. The [deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis)
page also lists Folder Sync (`GET /api/sync/{remoteRepositoryKey}/{folderPath}`), File
Compliance Info (`GET /api/compliance/{repoKey}/{path}`), Pull/Push Replication
(`POST /api/replication/{srcRepoKey}/{srcPath}`) and the legacy security API
([05](05-platform-identity-and-security-apis.md)).

Enterprise-only in JFrog's licensing: every federated endpoint and `rclass: federated`,
multi-push replication, signed URLs, `cdnRedirect`, and (Pro X and above) Open Metrics.
Features JFrog gates behind Pro, all in scope here: most searches, copy and move,
properties, file lists and `lastModified`, archive explode and download, the trash can,
replication, Docker `_catalog`, tags and promote, version tokens, and custom layouts.

## Sources

- <https://docs.jfrog.com/artifactory/reference/repositories-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/artifacts-storage-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/searches-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/repository-replication-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/trash-cangarbage-collection-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/package-type-specific-apis/llms.txt>
- <https://docs.jfrog.com/artifactory/reference/deployartifact.md>, <https://docs.jfrog.com/artifactory/reference/getstorageitem.md>, <https://docs.jfrog.com/artifactory/reference/updateitemproperties.md>, <https://docs.jfrog.com/artifactory/reference/deleterepository.md>, <https://docs.jfrog.com/artifactory/reference/checkifrepositoryexists.md>, <https://docs.jfrog.com/artifactory/reference/searchlatestversionbyproperties.md>
- <https://docs.jfrog.com/integrations/reference/promotedockerimage.md>
- Status codes the clients require: <https://github.com/jfrog/jfrog-client-go> (`artifactory/services`) and <https://github.com/jfrog/terraform-provider-artifactory>
