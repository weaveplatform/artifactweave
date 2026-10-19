# Artifactory core REST API inventory

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))

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
| GET | `/api/repositories?type=&packageType=&project=` | List repositories | 200 | Admin or scopes `artifact:*:r` / `system:info:r`. `type` is `local`, `remote`, `virtual`, `federated` or `distribution`; `packageType` accepts any value (the documented enum is stale) and matches the configured type or the listed name. Body `[{key,type,description,url,packageType}]`, where `packageType` is the `RepoType` name, such as `Generic`, `Npm` or `YUM` for rpm ([research 03](03-repository-configuration-model.md#package-type-names-in-listings)); media type `...RepositoryDetailsList+json`. |
| GET | `/api/repositories/{remoteRepoName}/configuration` | Shared remote configuration | 200 | Deprecated; use get configuration. |
| DELETE | `/api/repositories/{repoKey}` | Delete repository | 200; 202 deletion already running; 207 partial, configuration kept; 400 blank, trash or support-bundle key; 403 not an admin, a validation failure such as the build-info repository, or every item failed; 404; 409 import in progress or worker veto; 424 federation member removal failed | Admin. Removes configuration and content. The Terraform provider requires exactly 200. The success body is JSON from 7.161.15, plain text before ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)); artifactweave answers JSON. |
| DELETE | `/api/v2/repositories/batch` | Delete several repositories | 200 all deleted; 207 mixed; 400 fail-fast; 403 fail-fast or all failed; 409, 424, 500 all failed | Body `["a","b"]`. Response `RepoBulkRemovalReport{reports[],statusMessage}`; when every repository failed, `statusMessage` is "Repositories batch removal failed". The fail-fast answers delete nothing and carry only `statusMessage`: 400 "No repository keys were provided for deletion" for `[]`, 400 for a blank, trash or support-bundle key, and 403 "Deletion of Build Info repository artifactory-build-info is not allowed." ([deleterepositories](https://docs.jfrog.com/artifactory/reference/deleterepositories.md), examples `emptyRepoKeyList`, `invalidSystemRepo` and `buildInfoBlocked`). New in 7.161.15 ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)). |

## Artifacts and storage API

Source: <https://docs.jfrog.com/artifactory/reference/artifacts-storage-api/llms.txt>.

| Method | Path | Operation | Status | Notes |
|---|---|---|---|---|
| GET | `/{repoKey}/{filePath}` | Retrieve artifact | 200; 206 with `Range`; 302 when `downloadRedirect`; 404 | Read permission, anonymous allowed when enabled. Query `skipUpdateStats`, `trace` (text explanation of resolution). Matrix parameters `;key=value` make resolution conditional on properties. Version tokens `[RELEASE]`, `[INTEGRATION]`, `SNAPSHOT` resolve the latest matching version (layout dependent). Response headers `X-Checksum-Md5`, `X-Checksum-Sha1`, `X-Checksum-Sha256`, `Accept-Ranges: bytes`. |
| PUT | `/{repoKey}/{filePath}` | Deploy artifact, create folder | 201 with file info; 400 malformed path; 405 target is virtual without deployment repo; 409 checksum mismatch or duplicate | Deploy permission. Body `application/octet-stream`. Headers `X-Checksum-Sha1`, `X-Checksum-Sha256`, `X-Checksum-Md5` and `X-Checksum` (sha256) are verified. A trailing `/` with no body creates a folder (the CLI expects 201). Matrix parameters set properties. The file info's `uri` is the download URL, the same as `downloadUri` ([deployartifact](https://docs.jfrog.com/artifactory/reference/deployartifact.md) example); a folder's `path` and `uri` end in `/` (6.20's `SuccessfulDeploymentResponseHelper` **[I]**). Replacing a file needs Delete/Overwrite as well, except when the request declares the file's SHA-1 or SHA-256 and carries the same content ([known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues), 7.26.3, RTDEV-24969; 6.20's `DbStoringRepoMixin.isFileOverwrite` **[I]**). |
| PUT | same with `X-Checksum-Deploy: true` | Deploy by checksum | 201 when the caller can read an existing artifact with the checksum; 404 otherwise | Over a file with the same content it needs deploy only, unless `enforce.permission.check.on.identical.checksum.deploy=true` (RTDEV-24969 above). Checksum header is `X-Checksum-Sha1`, `X-Checksum-Sha256` or `X-Checksum` (type by length 32, 40 or 64). Content the caller cannot read answers the same 404 as content the server lacks ([deployartifactbychecksum](https://docs.jfrog.com/artifactory/reference/deployartifactbychecksum.md): "If Artifactory already contains a user-readable artifact with the same checksum, the artifact content is copied ... Otherwise, a 404 error is returned"). The checksum deploy that ends a multipart upload also sends `X-Checksum-Deploy-Token` (**[S]** jfrog-client-go `upload.go`, `utils.AddChecksumTokenHeader`); here that token passes for the user who made the upload and its content only, because no artifact holds the content yet. On 404 the client uploads normally; the CLI treats any non-2xx the same way. |
| PUT | same with `X-Explode-Archive: true` | Deploy from archive | 201 `text/plain` | zip, tar, tar.gz, tgz. `X-Explode-Archive-Atomic: true` for all-or-nothing. Only the extracted content is stored. Disables checksum deploy in the CLI. |
| GET | `/{repoKey}/{archivePath}!/{entryPath}` | Archive entry download | 200; 404 | Read permission. `!` then `/` is required. |
| DELETE | `/{repoKey}/{filePath}` | Delete item | 204; 404 | Delete permission. Folders delete recursively. Works on remote caches. |
| GET | `/api/storage/{repoKey}/{itemPath}` | Item information | 200; 404 | Without a mode: file or folder info. `?list[&deep=0\|1&depth=N&listFolders=0\|1&mdTimestamps=0\|1&includeRootPath=0\|1]` needs a non-anonymous user. `?stats`. `?lastModified` needs deploy permission and for folders returns the newest item recursively. `?properties[=k1,k2]` (404 "No properties could be found." when none, which the JFrog CLI reads as no properties). `?permissions`. |
| PUT | `/api/storage/{repoKey}/{itemPath}?properties=k1=v1[,v2]\|k2=v3[&recursive=0\|1]` | Set properties | 204 | Local and cache items only. `recursive` defaults to 1. Separate properties with `;` or `\|`, values with `,`; escape `,`, `\`, `\|` and `=` with `%5C`. An empty item path names the repository itself, which keeps properties of its own; this and the read, delete and update of properties work on it as on a folder ([setitemproperties](https://docs.jfrog.com/artifactory/reference/setitemproperties.md): "file, folder, repository, or Release Bundle v2"). |
| DELETE | `/api/storage/{repoKey}/{itemPath}?properties=k1,k2[&recursive=0\|1]` | Delete properties | 204; 404 when the item or repository does not exist | `recursive` defaults to 1. The JFrog CLI's `delete-props` accepts only 204. It tolerates the 404 "No properties could be found" only when it reads properties (`GetItemProperties` in jfrog-client-go `artifactory/services/props.go`). |
| PATCH | `/api/metadata/{repoKey}/{itemPath}?recursiveProperties=0\|1&atomicProperties=0\|1` | Update properties | 204; 400 when both `props` and `stats` are missing | Annotate permission. Body `{"props":{"k":"v" \| ["v1","v2"] \| null},"stats":{...}}`; `null` removes the key. `stats` sets a file's download statistics. Used by the Terraform `artifactory_item_properties` resource. 6.1.0+. |
| POST | `/api/checksum/sha256` | Set sha256 property | 200 | Admin. Body `{"repoKey","path"}`. Legacy: sets a `sha256` property recursively for pre-5.5 content. |
| POST | `/api/copy/{srcRepo}/{srcPath}?to=/{dstRepo}/{dstPath}[&dry=1][&suppressLayouts=0\|1][&failFast=0\|1]` | Copy item | 200; 400 with the same body when the parameters are invalid or the source does not exist ([copyitem](https://docs.jfrog.com/artifactory/reference/copyitem.md)) | Local, remote cache and federated sources. `suppressLayouts` defaults to 1; `dry` to 0. Body `{"messages":[{"level":"INFO\|WARNING\|ERROR","message":"..."}]}` (the `CopyOrMoveMessage.level` enum in [copyitem](https://docs.jfrog.com/artifactory/reference/copyitem.md)), media type `...storage.CopyOrMoveResult+json`. A copy or move keeps the item's creation and last-modified times; 7.125.4 added a feature flag, off by default, that gives the target the operation's time as its creation time ([7.125.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)). artifactweave keeps a copied or moved file's creation and last-modified times too, and does not offer the flag ([DV-062](../implementation/deviations.md)). Who created and last modified it, and when it was last updated, are kept as well: 7.x documents none of them, and 6.20's mover copies the whole item record (`DbMutableItem.fillInfo` in `knottnhupfer/artifactory-oss` at `b99e5649bcad`) **[I]**. A folder a copy creates is new, created by the caller; one a move creates keeps its own times ([#349](https://github.com/weaveplatform/artifactweave/issues/349)). |
| POST | `/api/move/{srcRepo}/{srcPath}?to=...` | Move item | 200; 400 as for copy ([moveitem](https://docs.jfrog.com/artifactory/reference/moveitem.md)) | Local repositories only. Same parameters and body as copy. |
| GET | `/api/archive/download/{repoKey}/{path}?archiveType=zip\|tar\|tar.gz\|tgz[&includeChecksumFiles=true]` | Download folder as archive | 200; 400 while the most folder downloads that may run at once are running; 403 when folder download is disabled | Requires the "Enable Folder Download" setting; local and cache only; `archiveType` is mandatory. "Max Parallel Folder Downloads" caps the downloads that run at once, 10 by default. |
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
`{"uri","created","files":[{"uri":"/rel/path","size":-1 for folders,"lastModified","folder","sha1","sha2","mdTimestamps":{"properties":"<ISO time>"}}]}`:
- `size` is a JSON number. JFrog's reference example shows a string, but jfrog-client-go
  decodes it as `json.Number`, and the Terraform `artifactory_file_list` data source decodes
  it as `int64`. The data source's acceptance test expects `-1` for a folder **[S]**
  (`datasource_artifactory_file_list.go` in `jfrog/terraform-provider-artifactory`).
- Files carry `sha2`, the SHA-256. With `mdTimestamps=1`, an item with properties reports
  when they last changed as `properties`. jfrog-client-go's `fileListTest`, which runs
  against a live Artifactory, asserts both for `a.in`, which `uploadDummyFile` deploys with
  `dummy=yes` as a matrix parameter, so a deploy that carries properties sets the time
  **[S]** (`tests/artifactorystorage_test.go` and `tests/utils_test.go` at `3b4b900d8174`).
  7.x's AQL keeps the same time as `props_modified` in its item info domain
  ([22](22-aql-searches-build-info.md)). 7.x's documentation, release notes and clients say
  nothing more, so the rest follows 6.20 **[I]**: an item without properties has no
  `mdTimestamps`, because `FileListElement` creates the map only when `addMdTimestamp` adds
  a time; a write that leaves the properties as they were keeps the time, and one that
  leaves none removes it, because `DbMutableItem.save` stamps its `node_meta_infos` row
  only when `PropertiesService.setProperties` reports a change and deletes the row when no
  properties are left; and a copy, move or trash restore gives the item it writes a new
  time, because it saves a new node (`knottnhupfer/artifactory-oss` at `b99e5649bcad`).
- `includeRootPath=1` ("Include folder root path",
  [getstorageitem](https://docs.jfrog.com/artifactory/reference/getstorageitem.md)) adds an
  entry for the listed folder itself. Listing a repository root, the Terraform data
  source's `TestAccDataSourceFileList_include_root_path` expects it first, with `uri` `/`,
  `size` `-1` and `folder` true, and the other entries with their `uri` relative to the
  root as without the parameter **[S]**. jfrog-client-go's `fileListTest` agrees: four items
  list as five entries, with `a.in` at `/test/a.in` **[S]**. Below the root, the folder's
  entry is `/` as well and the others stay relative to it, and the entry is added whether or
  not `listFolders=1` **[I]**.

Stats are `{"uri","lastDownloaded":<epoch ms>,"downloadCount","lastDownloadedBy"}`.
Properties are `{"uri","properties":{"k":["v1","v2"]}}` and values are always arrays.
Permissions are `{"uri","principals":{"users":{},"groups":{}}}`.

The storage summary is:

```json
{"binariesSummary":{"binariesCount":"125,726","binariesSize":"3.48 GB","artifactsSize":"4.10 GB","optimization":"84.9%","itemsCount":"...","artifactsCount":"..."},
 "fileStoreSummary":{"storageType":"file-system","storageDirectory":"/data","totalSpace":"...","usedSpace":"...","freeSpace":"..."},
 "repositoriesSummaryList":[{"repoKey":"auto-trashcan","repoType":"NA","foldersCount":0,"filesCount":0,"usedSpace":"0 bytes","usedSpaceInBytes":0,"itemsCount":0,"packageType":"NA","projectKey":"default","percentage":"0%"},
                            {"repoKey":"npm-local","repoType":"LOCAL","foldersCount":1,"filesCount":2,"usedSpace":"1.00 KB","usedSpaceInBytes":1024,"itemsCount":3,"packageType":"npm","projectKey":"default","percentage":"100%"},
                            {"repoKey":"TOTAL","repoType":"NA","foldersCount":1,"filesCount":2,"usedSpace":"1.00 KB","usedSpaceInBytes":1024,"itemsCount":3}]}
```

Numbers are human-formatted strings, except the counts and `usedSpaceInBytes`. The trash can
has a row of its own, `auto-trashcan`, and a `TOTAL` row closes the list. The formats, the
access rule and their evidence are in
[research 15, section 2.2](15-data-model.md#22-item-artifactory-artifacts-storage-api).

Checksum sidecars `.sha1`, `.md5` and `.sha256` are served next to every artifact and are
not items themselves. Under `client-checksums` a sidecar serves the client's checksum, and
answers 404 while the client has sent none; every deploy records the SHA-256 as the client's,
so the `.sha256` file is always served. Under `server-generated-checksums`, and for
`maven-metadata.xml`, a sidecar serves the computed checksum
([#399](https://github.com/weaveplatform/artifactweave/issues/399), the 7.x check below).
artifactweave also serves `.sha512`, which is its own extension (a
deviation registered by #378). No 7.x page or release note documents SHA-512 for generic or
Maven files, and an open request asks JFrog to add it for generic files
(**[J]** [RTFACT-30948](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30948)). With
`downloadRedirect` enabled a download answers 302 to a signed object-store URL.

### Artifactory 7.x check

The audit of P1 to P7 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))
read this section again on 2026-10-18. Its sources were the reference pages above, the
release notes and known issues, KB articles, JFrog's Jira, jfrog-client-go `master` at
`3b4b900d8174` with its live integration tests, jfrog-cli-core, and the Terraform
provider's acceptance tests. The rows hold for 7.x with the corrections made in them. The
checksum deploy matches 7.x: it reuses stored content only for a caller that can read an
existing artifact with that checksum, and answers 404 otherwise
([#379](https://github.com/weaveplatform/artifactweave/issues/379)).
Checksum sidecars match 7.x
([#399](https://github.com/weaveplatform/artifactweave/issues/399)). Under `client-checksums`,
`GET <file>.sha1` or `.md5` serves the client's checksum, and answers 404 while the client
has sent none (**[KB]** [Understanding Artifactory Checksum Policies](https://jfrog.com/help/r/what-are-client-checksum-server-checksum-and-checksum-policy-in-local-repositories/what-are-client-checksum-server-checksum-and-checksum-policy-in-local-repositories),
modified 2026-10-11). Every deploy records the SHA-256 as an original checksum, including when
the client sent none (**[KB]** the same article), and a checksum deploy records all the
checksums of the content it reuses: since
[7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)
(RTDEV-80481), a checksum deploy that sends only a SHA-256 no longer reports "Client did not
publish a checksum value". The 404's message, "Checksum not found for <path>", is 6.20's
(`DownloadServiceImpl.respondForChecksumRequest`) **[I]**.

Settled since the audit:
- The storage summary reads as 7.x's ([#390](https://github.com/weaveplatform/artifactweave/issues/390)). An administrator or a token scoped `system:info/storage:r` reads it ([getstoragesummaryinfo](https://docs.jfrog.com/artifactory/reference/getstoragesummaryinfo.md); [7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed)). Each row carries `usedSpaceInBytes` and `projectKey` (**[S]** jfrog-client-go's live `storageInfoTest`; jfrog-cli-core `artifactory/utils/storageinfo.go` reads `usedSpaceInBytes` first), and `packageType` is the display name. Sizes keep two decimals and group thousands, a share of nothing is `N/A`, an `auto-trashcan` row counts the trash can, the TOTAL row has no `packageType`, `percentage` or `projectKey`, and `storageType` names the binary provider template. `optimization` is `binariesSize` over `artifactsSize`, and an object store's `storageDirectory` is "Filesystem storage is not used" ([#461](https://github.com/weaveplatform/artifactweave/issues/461)). A blob mirror's is the directory of its first `fs` member, with that directory's space, as 6.20 reports a chain's first local directory **[I]** ([#490](https://github.com/weaveplatform/artifactweave/issues/490)). [Research 15, section 2.2](15-data-model.md#22-item-artifactory-artifacts-storage-api) has the captures behind each, and what stays **[I]**.
- A deploy answers `uri` with the download URL, the same as `downloadUri`, and a folder's `path` and `uri` end in `/` ([deployartifact](https://docs.jfrog.com/artifactory/reference/deployartifact.md) example and its checksum-deploy page; the trailing slash is from 6.20's `SuccessfulDeploymentResponseHelper` **[I]**). Every deploy that answers with the file info does so: a file, a checksum deploy, a deploy with matrix parameters and a folder ([#400](https://github.com/weaveplatform/artifactweave/issues/400)).
- An archive deploy answers 201 `text/plain` with no body ([deployartifactsfromarchive](https://docs.jfrog.com/artifactory/reference/deployartifactsfromarchive.md) documents the type and gives no text). The body is empty because jfrog-client-go decodes any non-empty body an upload answers as JSON (**[S]** `ExtractSha256FromResponseBody`, called after every upload in `artifactory/services/upload.go`), so `jf rt u --explode` would fail on any text **[I]**. Every successful deploy sets `Location` to the URL of what it stored: the file, a format's renamed file such as a Maven unique snapshot, a folder without its trailing `/`, or the folder an archive was extracted into. 7.x documents no `Location`; 6.20 sets it on every deploy (`SuccessfulDeploymentResponseHelper`) **[I]** ([#474](https://github.com/weaveplatform/artifactweave/issues/474)).
- Replacing a file with its own content needs deploy alone. A deploy that declares the stored file's SHA-1 or SHA-256, with neither one different, needs no Delete/Overwrite, and so does such a checksum deploy unless `enforce.permission.check.on.identical.checksum.deploy=true` ([known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues), 7.26.3, RTDEV-24969). 6.20's `DbStoringRepoMixin.isFileOverwrite` compares the declared checksums this way **[I]**. artifactweave's setting is `deploy.enforce_permission_check_on_identical_checksum_deploy`, off by default ([configuration reference](../operations/configuration-reference.md)). artifactweave also checks that the content it stores is the file's, because a declaration is only the client's word, and nothing checks it under `server-generated-checksums`. It checks once it holds the content: after the checksum policy, and for a checksum deploy after the read check of [#379](https://github.com/weaveplatform/artifactweave/issues/379). 6.20 compares the declaration before it reads the body, so a caller that may deploy but not read can learn from a 403 whether a checksum it guessed is the file's. Here only a caller that holds the content can learn that the file has it. So a refused overwrite whose body also fails its own declared checksum answers 409, not 403 ([#400](https://github.com/weaveplatform/artifactweave/issues/400)).
- A copy or move whose source does not exist, its repository included, answers 400 with a `CopyOrMoveResult` body whose one `ERROR` message names the source ([copyitem](https://docs.jfrog.com/artifactory/reference/copyitem.md), [moveitem](https://docs.jfrog.com/artifactory/reference/moveitem.md): "The request parameters are invalid or the source path does not exist"). A caller that may not read the source path gets 403 first, so the answer says nothing about a path it may not see. The message's wording is not published **[I]** ([#400](https://github.com/weaveplatform/artifactweave/issues/400)).
- Properties can be set on, read from, updated on and deleted from a repository itself, with an empty item path. `recursive=1` sets them on the repository and on everything in it; `recursive=0` on the repository alone. The evidence is [setitemproperties](https://docs.jfrog.com/artifactory/reference/setitemproperties.md) ("file, folder, repository, or Release Bundle v2"; the delete and update pages say "file, folder, or repository"), **[S]** the Terraform provider's `TestAccItemProperties_repo_only`, which runs against live Artifactory and reads two properties back from a repository with no `item_path`, and **[J]** [RTFACT-29810](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29810), which sets "repository-level properties" on a repository and found a trash can restore adding them to the restored items. A restore in artifactweave brings back only the item's own properties and the two every restore adds ([trash can](#trash-can-and-garbage-collection)). artifactweave keeps them on a record of the repository root ([0004](decisions/0004-storage-sql-metadata-and-blobs.md), [#388](https://github.com/weaveplatform/artifactweave/issues/388)). Deleting a repository's content keeps them, as the root is the repository itself **[I]**.
- The `?list` form matches 7.x as described above: `size` is a JSON number, `-1` for a folder; file entries carry `sha2`; `mdTimestamps=1` reports when an item's properties last changed, which artifactweave records on every route that writes properties (set, delete, `PATCH /api/metadata`, a deploy with matrix parameters, copy, move, trash restore, and npm's own writes of `npm.*` properties and dist-tags); and `includeRootPath=1` adds the listed folder as `/` ahead of the other entries, which keep their relative `uri` ([#389](https://github.com/weaveplatform/artifactweave/issues/389)). The evidence is **[S]** the Terraform `artifactory_file_list` data source, which decodes `size` as an `int64`, with its acceptance tests, and jfrog-client-go's `fileListTest`. What 6.20 alone shows is marked **[I]** there.
- `GET /{repoKey}/{filePath}?trace` answers 200 `text/plain` with the resolution in place of the content: it "simulates an artifact retrieval request and returns verbose output about the resolution process" ([retrieveartifact](https://docs.jfrog.com/artifactory/reference/retrieveartifact.md)). Its shape is the sample in JFrog's REST API reference: the lines `Request ID`, `Repo Path ID`, `Method Name`, `User`, `Time` and `Thread`, then `Steps:` and one line per step, each starting with its time ([archived REST API page](https://web.archive.org/web/2021/https://www.jfrog.com/confluence/display/JFROG/Artifactory+REST+API), Trace Artifact Retrieval). The steps use 7.x's wording, such as "Searching for the resource within libs-snapshot-local" and "Sending a response with the status '404' and the message 'Could not find resource'" (**[KB]** [The ?trace API](https://jfrog.com/help/r/artifactory-uncovering-virtual-repository-resolution/the-trace-api), 7.x output from 2022). artifactweave writes the steps of its own resolution: the repository, each virtual member it searches, a path the format rewrites, the item found, and the status and message the download would answer. It leaves out the `Thread` line, as Go names no request threads. It answers 200 whatever the download would answer, and serves, redirects and counts nothing, as 6.20's `TraceLoggingResponse` does **[I]**. A caller that may not read the path sees only the refusal.
- `PATCH /api/metadata` accepts `props`, `stats` or both, and answers 400 only when both are missing ([updateitemproperties](https://docs.jfrog.com/artifactory/reference/updateitemproperties.md): 400 when "props/stats fields are missing"). The page names no `stats` fields. artifactweave merges `downloadCount`, `lastDownloaded`, `lastDownloadedBy`, `remoteDownloadCount`, `remoteLastDownloaded` and `remoteLastDownloadedBy` into a file's statistics, with times in Unix milliseconds as `?stats` shows them, as 6.20's `ArtifactMetadataResource` merges the body into `StatsInfo` **[I]**. A field left out keeps its value and `null` clears it. A folder has no statistics, so `stats` for a folder answers 400, and so does an unknown field **[I]**. Statistics need Annotate, as properties do, which the page states for the operation. An empty `props` changes nothing and answers 204. A body with neither field answers 400 with 6.20's "props or stats fields required" **[I]**.
- At most "Max Parallel Folder Downloads" folder archive downloads run at once ([general settings](https://docs.jfrog.com/installation/docs/general-settings), Folder Download Settings; the YAML key `folderDownloadConfig.maxConcurrentRequests`). artifactweave sets it per node with `download.folder.max_concurrent_requests`, 10 by default as in 6.20, because it serves no folder download settings API ([configuration reference](../operations/configuration-reference.md)). One more answers 400 "There are too many folder download requests currently running. Try again later." after every other check, as 6.20's `FolderDownloadServiceImpl` answers **[I]**.

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
| POST | `/api/trash/restore/{repoKey}/{path}?to=<repo/path>[&transaction-size=N]` | Restore an item | 202 text/plain | Manage on the destination ([restoreitemfromtrashcan](https://docs.jfrog.com/artifactory/reference/restoreitemfromtrashcan.md)), and delete or manage on the repository the items were deleted from, as for clean. |
| POST | `/api/deleteDirectoryTrashProperty/start` | Remove redundant trash folder records | 200 | JSON body. |
| POST | `/api/system/storage/gc` | Run garbage collection | 200 | Admin. |

The audit of P1 to P7 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))
confirmed these rows for 7.x.

A restore needs permission on the repository the items were deleted from as well as on the
destination. From 7.41.4 a non-admin restores only items deleted from repositories it holds
delete permission on, and cannot restore any other repository's
([general settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring
Deleted Repositories";
[7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed),
RTDEV-58624). 7.x refuses a restore without it with "User doesn't have permissions to move
'auto-trashcan:docker-v2-local/PROJECT/COMPONENT'. Needs delete permissions."
(**[J]** [RTFACT-29653](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29653), open,
affects 7.55.13). The same issue reports that 7.x wants delete on the whole origin
repository, so a folder-level grant does not let its holder restore that folder **[I]**.
artifactweave checks delete or manage on the origin at the requested path and at every
restored item's original path, and requires Manage on the destination as the reference page
states ([decision 0010](decisions/0010-retention-trash-and-gc.md#artifactory-7x-check)).

The trash can is listed through the storage API under the `auto-trashcan` key. From 7.41.4
a non-admin with deploy or manage on a repository sees that repository's trashed items, and
only those, and one holding neither on any repository does not see the trash can
([general settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring
Deleted Repositories"; **[J]** [RTFACT-27280](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27280),
describing [RTFACT-13242](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-13242), Done).
artifactweave judges each trashed item at its original path. A signed-in caller that sees
nothing at a path is refused with 403 before the trash is read, and folder listings leave out
what it does not see, as for an unreadable path elsewhere in the storage API **[I]**.
Anonymous gets 401, and only an admin sees what was trashed from a repository that no longer
exists **[I]**.

The trash can serves the storage API's views as any repository does
([#455](https://github.com/weaveplatform/artifactweave/issues/455)). Its root holds a folder
per repository with trashed items, and below each lie the trashed paths. JFrog documents the
file list there with all its parameters, `deep`, `depth`, `listFolders`, `mdTimestamps` and
`includeRootPath` (**[KB]** [How to search artifacts under trash can using REST
API](https://jfrog.com/help/r/artifactory-how-to-search-artifacts-under-trash-can-using-rest-api),
2025; **[KB]** [How to use REST API to list items in the trash
can](https://jfrog.com/help/r/how-to-use-rest-api-to-list-items-in-the-trash-can/how-to-use-rest-api-to-list-items-in-the-trash-can)).
No 7.x source shows the other views there, so they follow the trash can being a repository
**[I]**:
- the item info, as elsewhere, with a trashed file's `trash.*` properties added;
- `?list`, with each trashed item's properties last changed when it was trashed;
- `?properties`, the item's own properties and the `trash.*` ones, as 6.20 keeps both on the
  item in the trash can (`TrashServiceImpl.buildDeletedItemProperties`). A folder that holds
  only what was trashed below it has none;
- `?stats`, a download count of 0, as 6.20 trashes a copy of the file, which nothing has
  downloaded;
- `?lastModified`, the newest item below a folder.

`?permissions` answers 400 to an administrator, as no permission target can name the trash
can (**[J]** [RTFACT-30167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30167), open)
**[I]**. Every view applies the filter above: a caller that is not an administrator sees only
what was trashed from repositories where it holds deploy or manage, and is refused with 403
before the trash is read at a path where it sees nothing.

While the trash can is off, every one of these views, the root included, answers 404 "Could
not find resource" to every caller, an administrator too, as a path that is not there does
([#490](https://github.com/weaveplatform/artifactweave/issues/490)). 7.x fixed its trashed
files being reachable through the native browser while the trash can was off
([SaaS 7.170.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71700-saas),
RTDEV-97807); the status and message are **[I]**. What the trash can held stays. `restore`,
`clean` and `empty` still act on it, as 6.20's trash resources check no setting **[I]**, and
nothing in it expires until the trash can is on again: "The Small Garbage Collector won't
work if the Trash Can is disabled" (**[KB]** [The complete Garbage Collection
Guide](https://jfrog.com/help/r/artifactory-artifactory-garbage-collection)).

A trashed item carries the properties `trash.time`, `trash.deletedBy`,
`trash.originalRepository`, `trash.originalRepositoryType` and `trash.originalPath`
([#477](https://github.com/weaveplatform/artifactweave/issues/477)). `trash.time` is the time
of the delete in Unix milliseconds: **[J]** [RTFACT-29920](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29920)
(Done, affects 7.61.1) compares `trash.time` with an epoch in milliseconds. The other values
are 6.20's (`TrashServiceImpl.buildDeletedItemProperties`) **[I]**: who deleted the item, the
key and package type of its repository, and its path there. The package type is
`RepoType.getType()`, the type's enumeration name in lower case, which is `yum` for rpm, as
7.x still names the type `YUM` ([research 03](03-repository-configuration-model.md#package-type-names-in-listings))
**[I]**. artifactweave records the package type when the item is deleted, so it is still known
once the repository is gone.

A restored item carries the properties `trash.originalRepository` and `trash.restoredTime` beside its own (**[J]** [RTFACT-29810](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29810), Done, affects 7.55.10: the expected and the observed properties of restored items both include them). Their values are 6.20's: the key of the repository the item was deleted from, and the time of the restore in Unix milliseconds (`TrashService`, `TrashInterceptor.afterMove`) **[I]**. artifactweave sets them on every item a restore brings back, folders included, and a restore time recorded by an earlier restore gives way to the new one **[I]**.

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
