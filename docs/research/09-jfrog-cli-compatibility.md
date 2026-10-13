# 09: JFrog CLI compatibility

What the JFrog CLI (`jf rt ...`) and its client library actually send, so that
artifactweave can serve it byte for byte: paths, headers, the generated AQL, the status
codes the client hard-checks, and the telemetry calls that must be absorbed.

Research baseline: **2026-10-12**. Sources: `jfrog-client-go` v1.55.0
(https://github.com/jfrog/jfrog-client-go) and `jfrog-cli-core` v2.60.0
(https://github.com/jfrog/jfrog-cli-core), read at the file paths cited per row. Items
marked *(unverified)* lack a primary source.

## Identity and headers

| Item | Value | Source |
|---|---|---|
| User-Agent | `jfrog-cli-go/<version>` for the `jf` binary (`SetUserAgent` in cli-core); `jfrog-client-go/<version>` is only the library default | `jfrog-client-go/utils/utils.go`, cli-core `utils/coreutils` |
| Authentication | `Authorization: Bearer <token>`, HTTP Basic, or `X-JFrog-Art-Api: <key>` | `jfrog-client-go/http/httpclient/client.go` |
| API-key recognition | a credential is treated as an API key when it starts with `AKCp8` and is at least 73 characters | `jfrog-client-go/http/httpclient/client.go` |
| Username from a token (`jf c add`) | the JWT payload is split on `.`, decoded with Go `base64.RawStdEncoding` and `sub` is read; the subject must contain `/users/<name>` or start with `jfrt@`; payloads containing `-`, `_` or `=` fail silently and the CLI prompts for a username | `jfrog-client-go/auth/authutils.go` |

## Telemetry and version

The CLI performs **no startup version check**. It calls `GET api/system/version` for usage
reporting and before uploads to decide whether multipart upload is available. There is no
caller of `api/system/status`; `api/system/service_id` is used only by storage-info style
commands.

| Call | Expected status | Notes |
|---|---|---|
| `GET api/system/version` | 200 | `{"version","revision","addons"}`; also `application/vnd.org.jfrog.artifactory.system.Version+json` |
| `POST api/system/usage` | 200 or 202 | sent when the server reports ≥6.9.0; failures are logged, never fatal |
| `POST /jfconnect/api/v1/backoffice/metrics/log` | 200 or 201 | sent by every command when the server reports ≥7.102; failures are debug-logged |

Sources: `jfrog-cli-core/common/commands/command.go`,
`jfrog-client-go/jfconnect/services/metrics.go`, `jfrog-client-go/artifactory/services/system.go`.

## Commands

All paths are relative to `<base>/` where `<base>` is `https://host/artifactory`, unless
they start with `/access`.

| Command | Calls | Expected status | Notes |
|---|---|---|---|
| `jf rt ping` | `GET api/system/ping` | 200 | body `OK` |
| `jf rt upload` | `PUT <repo>/<path>[;prop=val;...]` with headers `X-Checksum-Sha1`, `X-Checksum-Md5` and `X-Checksum` (sha256; Artifactory also accepts `X-Checksum-Sha256`) | 200, 201 or 202 | properties travel as matrix parameters |
| | checksum deploy: the same PUT with `X-Checksum-Deploy: true` first, for files of 10 KiB or more | 2xx means done; **any** other status falls through to the real upload | disabled when exploding archives |
| | `X-Explode-Archive: true` for `--explode`; trailing `/` creates a folder | 201 for the folder PUT | |
| | multipart, for files ≥200 MiB when the version is ≥7.82.2: `GET api/v1/uploads/config` | **200** with `{"supported":false}` disables cleanly; 404 or 501 fails the upload | `POST api/v1/uploads/create?repoKey=&repoPath=&partSizeMB=`, `.../urlPart?partNumber=`, `PUT <url>`, `POST .../complete?sha1=` (202, with `X-Checksum-Deploy-Token`), `.../status`, `.../abort` |
| `jf rt download` | AQL (below), then `GET <repo>/<path>`; `HEAD` reads `Content-Length`, `Accept-Ranges: bytes` and `X-Checksum-Md5/Sha1/Sha256`; split downloads send `Range: bytes=a-b` | 200, 206 for ranges | |
| `jf rt search`, `delete`, `copy`, `move`, `set-props`, `delete-props`, `download` spec resolution | `POST api/search/aql` with `Content-Type: text/plain`; `GET api/storage/<repo>/<path>[?list&deep=1&listFolders=1&depth=&includeRootPath=1]`; `GET api/storage/<repo>/<path>?properties`; `GET api/storageinfo`, `POST api/storageinfo/calculate` | 200 | build-scoped searches use the `artifact.module.build.*`, `dependency.module.build.*` and `release_artifact.release.*` fields and read the `<project>-build-info` repository (default `artifactory-build-info`) |
| `jf rt delete` | `DELETE <repo>/<path>` | **204** | `HEAD` before a retry |
| `jf rt copy`, `jf rt move` | `POST api/copy/<repo>/<path>?to=/<target>[&dry=1]`, `POST api/move/...` | **200** with `{"messages":[...]}` | `PUT <dest>/` pre-creates folders and must return **201** |
| `jf rt set-props`, `delete-props` | `PUT api/storage/<repo>/<path>?properties=k=v,k2=v2&recursive=0\|1` (each `k=v` URL-escaped); `DELETE api/storage/<repo>/<path>?properties=k,k2&recursive=` | **204** | on delete a 404 with body "No properties could be found" is tolerated |
| `jf rt build-publish` | `PUT api/build[?project=]` with `Content-Type: application/vnd.org.jfrog.artifactory+json` | 200, 201 or 204 | build-info is also stored at `artifactory-build-info/<name>/<number>-<timestamp>.json` |
| build reads and maintenance | `GET api/build/<name>/<number>[?project=]`, `GET api/build/<name>`, `GET api/builds/buildArtifacts/<name>/<number>/<repo>`; `POST api/build/delete`; `POST api/build/retention/<name>`; `POST api/build/rename/<from>?to=` | 200; delete 200 or 204; retention **204** | |
| `jf rt build-promote` | `POST api/build/promote/<name>/<number>[?project=]` with `Content-Type: application/vnd.org.jfrog.artifactory.build.PromotionRequest+json` and body `{status,comment,sourceRepo,targetRepo,dependencies,copy,failFast,dryRun,properties}` | 200 | |
| `jf rt docker-promote` | `POST api/docker/<srcRepo>/v2/promote` with `{targetRepo,dockerRepository,targetDockerRepository,tag,targetTag,copy}` | 200 | |
| `jf rt repo-create`, `repo-update`, `repo-delete`, `repo-template` | `PUT api/repositories/<key>` (create), `POST api/repositories/<key>` (update), `GET api/repositories[?type=&packageType=&project=]`, `GET api/repositories/<key>`, `DELETE api/repositories/<key>` | create, update and delete all expect **200** | repository JSON as in [03-repository-configuration-model.md](03-repository-configuration-model.md) |
| `jf rt replication-*` | `PUT api/replications/<repo>` with `application/vnd.org.jfrog.artifactory.replications.ReplicationConfigRequest+json`, `GET`, `POST`, `DELETE api/replications/<repo>`; `PUT api/replications/multiple/<repo>`; `POST api/replication/execute/<repo>` | 200 | |
| `jf rt user-*`, `group-*` | `GET/PUT/POST/DELETE api/security/users[/<name>]`; `GET api/security/lockedUsers`; `POST api/security/unlockUsers/<name>` (deprecated path form, no body); `GET/PUT/POST/DELETE api/security/groups[/<name>?includeUsers=]` | 200 or 201 accepted | Terraform prefers `/access/api/v2/users` on ≥7.84.3, so both families are served |
| `jf rt permission-target-*` | `GET api/v2/security/permissions`; `GET/POST/PUT/DELETE api/v2/security/permissions/<name>` with `{name, repo:{include-patterns,exclude-patterns,repositories,actions:{users:{u:[perm]},groups:{}}}, build:{}, releaseBundle:{}}` | POST 201 (409 treated as "exists"), PUT 200, GET 200, DELETE 200 | permissions `read, annotate, write, delete, manage, managedXrayMeta, distribute` |
| `jf rt access-token-create`, `jf c add` | `POST /access/api/v1/tokens` with `{grant_type,username,scope,expires_in,refreshable,audience,description,project_key,include_reference_token,refresh_token}`; `GET /access/api/v1/tokens[/{id}]`; `DELETE /access/api/v1/tokens/{id}`; `POST /access/api/v1/oidc/token` | 200 | response `{token_id,access_token,refresh_token,expires_in,scope,token_type:"Bearer",reference_token}` |
| legacy tokens and API keys | `POST api/security/token` (form-encoded create or refresh, still implemented and not on JFrog's deprecation page), `GET api/security/token`, `POST api/security/token/revoke`; `POST/PUT/GET/DELETE api/security/apiKey` | token 200; API-key POST **201** | artifactweave answers API-key creation with 400 |

Sources: `artifactory/services/{ping,system,upload,download,aqlquerybuilder,searchutil,delete,movecopy,props,buildinfo,discardBuilds,promote,dockerpromote,repository,deleterepository,createreplication,users,groups,permissiontarget,security}.go`,
`artifactory/services/utils/{artifactoryutils,multipartupload}.go`,
`access/services/accesstoken.go`.

## The generated AQL

`jf` resolves every file spec through `items.find`. The shape, from
`artifactory/services/utils/aqlquerybuilder.go`:

```text
items.find({"$or":[{"$and":[{"repo":"libs","path":{"$match":"org/*"},"name":{"$match":"*.jar"}}]}],
            "type":"any","@build.name":"my-build","path":{"$ne":"."}})
     .include("name","repo","path","actual_md5","actual_sha1","sha256","size","type","modified","created","property")
     .sort({"$desc":["created"]})
     .offset(0)
     .transitive()
     .limit(1000)
```

- Suffix order is `include`, `sort`, `offset`, `transitive`, `limit`.
- `"type":"any"` is sent unless the spec restricts to files or folders.
- `"path":{"$ne":"."}` is added when the repository root is excluded.
- Property criteria are `{"@key":"value"}` objects inside `$and` or `$or`.
- `"property"` is included only when properties are requested.
- `.transitive()` is accepted and ignored by artifactweave.
- The response is `{"results":[...],"range":{"start_pos","end_pos","total","limit"}}`.

Minimum AQL subset for the M1 milestone: the `items` domain, `$and` and `$or`, the
operators `$eq`, `$ne`, `$match`, `$nmatch`, `$gt`, `$lt`, property criteria `@key`,
`type` in `any | file | folder`, and the `include`, `sort`, `offset`, `transitive`, `limit`
suffixes. Build searches add the `builds` domain and the `artifact.module.build.*` fields
after build-info lands. Every query shape the CLI generates is captured as a fixture under
`pkg/aql/aqltest/testdata` and executed on both SQL dialects with identical ordered output
([ADR 0012](decisions/0012-search-and-aql.md)).

## Status-code contract summary

| Call | Required code |
|---|---|
| `DELETE <repo>/<path>` | 204 |
| `PUT <dest>/` (folder) | 201 |
| `POST api/copy`, `api/move` | 200 |
| `PUT`/`DELETE api/storage/...?properties` | 204 |
| `PUT/POST/DELETE api/repositories/<key>` | 200 |
| `POST api/build/retention/<name>` | 204 |
| `PUT api/build` | 200, 201 or 204 |
| `POST api/build/promote/...`, `api/docker/.../promote` | 200 |
| `POST api/v2/security/permissions/<name>` | 201 (409 = exists) |
| `POST api/security/apiKey` | 201 |
| `GET api/v1/uploads/config` | 200 |
| `POST /access/api/v1/tokens` | 200 |

Primary URLs: https://github.com/jfrog/jfrog-client-go/tree/master/artifactory/services,
https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/utils/aqlquerybuilder.go,
https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/utils/artifactoryutils.go,
https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/utils/multipartupload.go,
https://github.com/jfrog/jfrog-client-go/blob/master/access/services/accesstoken.go,
https://github.com/jfrog/jfrog-client-go/blob/master/auth/authutils.go,
https://github.com/jfrog/jfrog-client-go/blob/master/http/httpclient/client.go,
https://github.com/jfrog/jfrog-cli-core/blob/master/common/commands/command.go,
https://docs.jfrog.com/artifactory/reference/searchaql.md.
