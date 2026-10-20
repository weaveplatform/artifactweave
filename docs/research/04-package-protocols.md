# Package protocols

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); [the 7.x check](#artifactory-7x-check) records it.

Research baseline: 2026-10-12. This document records, for each of the eight ecosystems
the registry serves, the client-facing protocol Artifactory implements: the upstream
specification, the URL layout under `/artifactory`, how clients authenticate, where
Artifactory stores the files, the metadata the server must generate for local and virtual
repositories, and what a remote adapter must rewrite. It ends each section with the
facts that could not be confirmed and must be captured against a live Artifactory. The
JFrog-specific management endpoints (reindex, promote) are in
[02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md) and the
repository fields in [03-repository-configuration-model.md](03-repository-configuration-model.md).

`<base>` means `https://{host}/artifactory`. Embedded absolute URLs in every format are
produced from the resolved base URL (`X-JFrog-Override-Base-Url`, then forwarded headers,
then the configured base URL, then the request), as decided in
[decisions/0002](decisions/0002-spec-first-flat-crud-handwritten-item-paths.md).

## Docker and OCI

Upstream: OCI Distribution Specification v1.1 <https://github.com/opencontainers/distribution-spec/blob/main/spec.md>;
Docker registry token authentication <https://docs.docker.com/reference/api/registry/auth.md>.
JFrog: <https://docs.jfrog.com/artifactory/docs/docker-repositories.md>,
<https://docs.jfrog.com/artifactory/docs/oci-repositories.md>,
<https://docs.jfrog.com/artifactory/docs/additional-docker-information.md>,
<https://jfrog.com/help/r/artifactory-how-to-debug-docker-configuration-issues/docker-login>.

### URL layout

The canonical server path is `<base>/api/docker/<repo>/v2/...`. Clients reach it through
one of three access methods selected by the `dockerReverseProxyMethod` setting
(`SUBDOMAIN`, `REPOPATHPREFIX`, `PORTPERREPO`; the Terraform provider also allows `DIRECT`):

| Method | Client reference | What the server sees |
|---|---|---|
| Repository path | `host/<repo>/<image>:<tag>` | `/v2/<repo>/<image>/...`, rewritten by a proxy to `/api/docker/<repo>/v2/<image>/...` |
| Subdomain | `<repo>.host/<image>:<tag>` | `/v2/<image>/...` with the repository taken from `Host` |
| Port | `host:<port>/<image>:<tag>` | one listener per repository |

Docker, OCI and Helm OCI repository keys "cannot contain uppercase letters or an
underscore" ([repository management overview](https://docs.jfrog.com/artifactory/docs/repository-management-overview.md));
the server refuses such a key ([research 03](03-repository-configuration-model.md#key-rules)).
This project serves the repository-path form natively
on `/v2/<repo>/<image>/...` (decision [0003](decisions/0003-repository-model.md)).

### Authentication

`GET /v2/` without credentials answers 401 with
`WWW-Authenticate: Bearer realm="<base>/api/docker/<repo>/v2/token",service="..."`.
The client then requests the realm with `?service=...&scope=repository:<image>:pull,push`
and basic credentials and receives `{"token":"<jwt>","expires_in":3600}`, which it sends
as `Authorization: Bearer`. Basic authentication is also accepted directly, with an
identity or reference token as the password. `GET <base>/api/docker/<repo>/v2/auth`
(and the legacy `/v1/auth`) returns a `config.json` entry `{"auth":base64(user:pass),"email"}`.
Anonymous pulls work when anonymous access is enabled. The first anonymous 401 is logged
by Artifactory as a denied login by design.

### Endpoints

The OCI endpoints `end-1` to `end-14`: `GET /v2/`; `GET` and `HEAD` on
`/v2/<name>/blobs/<digest>` and `/v2/<name>/manifests/<reference>`;
`POST /v2/<name>/blobs/uploads/[?digest=][&mount=<digest>&from=<repo>]`; `PATCH`,
`PUT`, `GET` and `DELETE` on the upload location; `PUT /v2/<name>/manifests/<reference>`;
`GET /v2/<name>/tags/list[?n=&last=]` with `Link` pagination in ASCII order;
`DELETE` on manifests and blobs; `GET /v2/<name>/referrers/<digest>[?artifactType=]`,
which returns `application/vnd.oci.image.index.v1+json`, sets `OCI-Filters-Applied` and
must not answer 404 once supported. The newer `end-4c` (`POST .../uploads/?digest-algorithm=`)
and `end-7b` (`PUT .../manifests/<digest>?tag=a&tag=b`) exist in the current
specification. A manifest pushed with a `subject` must be acknowledged with the
`OCI-Subject` header, which Artifactory does from 7.90.1. Response headers are
`Docker-Content-Digest`, `Location`, `Range` and `OCI-Chunk-Min-Length`. Errors are
`{"errors":[{"code","message","detail"}]}` with the specification's codes
(`BLOB_UNKNOWN` through `TOOMANYREQUESTS`).

`GET /v2/_catalog[?n=&last=]` is not part of the OCI specification; it is the Docker
Registry V2 extension, and Artifactory serves it only as `GET /api/docker/<repo>/v2/_catalog`.

### Storage layout

`<repo>/<image>/<tag>/manifest.json` with the layers as `sha256__<hex>` files in the
same folder; multi-architecture images add `<repo>/<image>/<tag>/list.manifest.json`
with the platform manifests under the tag folder (7.81+); referrers are tracked in a
`referrers.json` in the image folder (7.90.1+); pushing a platform-specific image also
publishes `<tag>-<os>-<arch>` style tags (7.21.2+); image labels become properties on
`manifest.json`; in-progress uploads live under `<image>/_uploads/`.

### Virtual and remote

A virtual repository resolves by member order and pushes to `defaultDeploymentRepo`. A
remote to `https://registry-1.docker.io` needs `enableTokenAuthentication`, and
`externalDependenciesEnabled` allows foreign layers. The `oci` and `helmoci` package
types share the Docker code path; `helm`, `oras` and `cosign` log in with a username and
an identity token.

### Unverified, capture against a live Artifactory

- The `service` value in the `WWW-Authenticate` challenge: a JFrog knowledge-base page
  confirms the realm path, and a captured challenge in a public bug report shows
  `service` set to the host name rather than the repository key *(unverified)*.
- Token response: `{"token":...,"expires_in":3600}` confirmed; `service` is the addressed hostname with any non-default port, and the bare `/v2/` probe uses the repository key `null` ([research 17](17-undocumented-shapes.md)).

## npm

Upstream: <https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md>,
<https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md>,
the `libnpmpublish` and `npm` CLI sources. JFrog: <https://docs.jfrog.com/artifactory/docs/npm-repositories.md>.

### URL layout

The registry is rooted at `<base>/api/npm/<repo>/`.

| Path | Behaviour |
|---|---|
| `GET /<pkg>`, `GET /@scope%2fname` | The packument. Scoped names arrive with an encoded slash. With `Accept: application/vnd.npm.install-v1+json` the abbreviated form is returned (`name`, `modified`, `dist-tags`, `versions` with `dist.tarball`, `shasum`, `integrity`, `fileCount`, `unpackedSize`, `deprecated`, `hasInstallScript`); the full form adds `_id`, `_rev`, `time`, `users`, `readme`, `maintainers`, `author`, `repository`, `license`. `dist.tarball` must point at the serving repository. |
| `GET /<pkg>/<version or dist-tag>` | One version manifest. |
| `GET /<pkg>/-/<pkg>-<ver>.tgz`, `GET /@scope/name/-/@scope/name-<ver>.tgz` | The tarball; also the storage layout. A scoped tarball's file name keeps the scope ([research 25](25-npm.md) section 2.1). |
| `PUT /<pkg>` | Publish: `{_id,name,description,dist-tags,versions{v:{..dist{tarball,shasum,integrity}}},access,_attachments{"<name>-<ver>.tgz":{content_type,data:<base64>,length}}}`. One `PUT`, never retried. npm 11 and later read the packument first and refuse an existing version themselves unless `--force` is given; Artifactory answers a republish with 403 ([research 25](25-npm.md) section 2.1). |
| `PUT /<pkg>/-rev/<rev>`, `DELETE /<pkg>/-/<pkg>-<ver>.tgz/-rev/<rev>`, `DELETE /<pkg>/-rev/<rev>` | Unpublish steps, each after a `GET /<pkg>?write=true` for `_rev`. Deprecation is a `PUT /<pkg>` of the whole packument with `deprecated` set and no `_rev` ([research 25](25-npm.md) section 2.2). |
| `GET`, `PUT`, `DELETE /-/package/<pkg>/dist-tags[/<tag>]` | Distribution tags; the `PUT` body is a JSON string. A virtual repository merges `latest` by member order. |
| `GET /-/v1/search?text=&size=&from=&quality=&popularity=&maintenance=`, legacy `GET /-/all` | Search. |
| `PUT /-/user/org.couchdb.user:<user>` | Legacy login (`--auth-type=legacy`, or the fallback when web login answers 4xx or 500): `{_id,name,password,type:"user",roles,date}` to a body carrying `token` ([research 25](25-npm.md) section 4). |
| `POST /-/v1/login`, `GET /-/v1/done/<session>` | Web login, the documented current flow: the first returns `{loginUrl,doneUrl}`, the second answers 202 until a token is issued, then `{token}`. Deferred to #262. |
| `GET /-/ping` | `npm ping`. |
| `POST /-/npm/v1/security/advisories/bulk`, `POST /-/npm/v1/security/audits/quick`, `POST /-/npm/v1/security/audits` | `npm audit`; served only by a virtual repository that aggregates a remote supporting audit, forwarded to `artifactory.npm.default.audit.provider`. |
| `GET /-/npm/v1/keys` | `npm audit signatures` (7.83.1+, ECDSA key pairs). |

Request headers to tolerate: `Npm-Scope`, `Npm-In-CI`, `npm-auth-type`, `npm-session`.

### Storage and layout

Tarballs live at `<name>/-/<name>-<ver>.tgz` or `@scope/<name>/-/@scope/<name>-<ver>.tgz`.
These are npm's own paths, not Artifactory's `npm-default` layout, which is
`[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).tgz` ([research 25](25-npm.md)
section 5). Repository-level "enforce strict layout", on by default for new
local repositories, moves packages to that layout in a background conversion; only the
global `artifactory.npm.enforce.path` property rejects other paths. Metadata caches live
under `.npm/<repo>/<pkg>`, so include patterns must allow `.npm/**` and `.npm/keys.json`.

### Unverified, capture against a live Artifactory

- `GET/POST /-/npm/v1/tokens`, `DELETE /-/npm/v1/tokens/token/<id>`, `GET /-/npm/v1/user`
  *(unverified; documented for npmjs only)*.
- `GET <base>/api/npm/auth` exists: the JFrog CLI calls it when its configuration holds no
  access token, and writes the 200 body into `.npmrc` as it stands
  ([research 25](25-npm.md) section 4). Its body, and the scoped
  `GET <base>/api/npm/<repo>/auth/<scope>`, remain *(unverified; absent from the current
  pages)*.
- The exact bodies of the web-login `POST /-/v1/login` and `GET /-/v1/done` exchange, for
  #262.

## Go modules

Upstream: <https://go.dev/ref/mod#goproxy-protocol>, the checksum database design
<https://go.googlesource.com/proposal/+/master/design/25530-sumdb.md>. JFrog:
<https://docs.jfrog.com/artifactory/docs/go-modules.md>, the CLI publish code
<https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/go/publish.go>.

### URL layout

Rooted at `<base>/api/go/<repo>`:

| Path | Behaviour |
|---|---|
| `GET /<module>/@v/list` | Plain text, one canonical version per line, no pseudo-versions. |
| `GET /<module>/@v/<ver>.info` | `{"Version":"v1.2.3","Time":"<RFC 3339>"}`, optional `Origin`. |
| `GET /<module>/@v/<ver>.mod` | The `go.mod`; synthesise `module <path>` when absent. |
| `GET /<module>/@v/<ver>.zip` | Entries prefixed `<module>@<ver>/`; at most 500 MiB, `go.mod` and `LICENSE` at most 16 MiB. |
| `GET /<module>/@latest` | Optional; the same JSON as `.info`. |
| `GET /sumdb/sum.golang.org/supported` | 200 means the proxy serves the checksum database and the client never goes direct; 404 or 410 sends the client to `sum.golang.org`. Then `/sumdb/sum.golang.org/lookup/<module>@<ver>`, `/latest`, `/tile/<H>/<L>/<K>[.p/<W>]`, `/tile/<H>/data/<K>[.p/<W>]`. Artifactory controls this with `artifactory.go.sumdb.enabled` and `artifactory.go.sumdb.url.override`. |

Upper-case letters in module paths and versions are escaped as `!` followed by the
lower-case letter. With a comma-separated `GOPROXY` list, 404 and 410 mean "try the next
proxy" and any other error is terminal; with the `|` separator the client falls through on
any error.

### Publishing and storage

`jf go-publish` performs `PUT <base>/api/go/<repo>/<module>/@v/<ver>.zip;go.version=<ver>[;prop=val]`,
then `.mod`, then `.info`, expecting 201 and reading `X-Checksum-Sha256` from the response;
it sends the checksum headers. The storage layout is therefore
`<module>/@v/<ver>.{zip,mod,info}` *(inferred; [research 26](26-go-modules.md) section 4.2)*.

### Remote and virtual

A remote Go repository is usable only through a virtual repository. The upstream is
`https://proxy.golang.org` (Git provider `ARTIFACTORY`) or a VCS provider from which
Artifactory builds archives from tags (out of scope initially). The virtual follows
`go-import` meta tags under an allow list. Credentials go in the `GOPROXY` URL or
`.netrc`.

### Unverified

The GOPROXY protocol is fully specified and the deploy paths are confirmed by the CLI
source, but Artifactory's Go handler is closed source. How it stores a version, what it
lists and answers for incomplete or republished versions, its errors and its `go-default`
layout are inferred: [research 26](26-go-modules.md) section 6 lists them, and they are on
the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

## Maven, Gradle, Ivy and SBT

Upstream: <https://maven.apache.org/repositories/layout.html>. JFrog:
<https://docs.jfrog.com/artifactory/docs/maven-repositories.md>,
<https://jfrog.com/help/r/artifactory-how-does-the-max-unique-snapshots-parameter-work-in-artifactory/artifactory-how-does-the-max-unique-snapshots-parameter-work-in-artifactory>,
<https://docs.jfrog.com/artifactory/reference/calculatemavenmetadata.md>.

### URL layout

Plain paths with no `/api/` prefix:
`<base>/<repo>/<group/as/dirs>/<artifactId>/<version>/<artifactId>-<version>[-<classifier>].<ext>`.
Deploy is a `PUT` to the same path; clients also `PUT` the `.sha1` and `.md5` sidecars and
`maven-metadata.xml` with its checksums. Authentication is basic from `settings.xml`;
"force authentication" on a virtual repository turns off anonymous reads and optional
non-preemptive mode is supported.

### Checksums

The client deploys `.md5` and `.sha1`; the server exposes `.sha256` as well and sets
`X-Checksum-*` response headers. artifactweave also serves `.sha512`, which is its own
extension, not Artifactory behaviour (a deviation registered by #378; **[J]**
[RTFACT-30948](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30948) is an open request
to add SHA-512 for generic files). `checksumPolicyType` decides whether a
mismatch between the client's sidecar and the computed value is rejected
(`client-checksums`) or replaced (`server-generated-checksums`). Sidecars are served from
metadata and are not items. Under `client-checksums` a sidecar serves the client's value, and
answers 404 while the client has sent none
([research 27 section 4](27-maven.md#4-checksums)).

### Generated metadata

| File | Content |
|---|---|
| `<g>/<a>/maven-metadata.xml` | `versioning` with `latest`, `release`, `versions` and `lastUpdated`. |
| `<g>/<a>/<v>-SNAPSHOT/maven-metadata.xml` | `snapshot` with `timestamp` (`yyyyMMdd.HHmmss`) and `buildNumber`, and `snapshotVersions` entries with `extension`, `classifier`, `value`, `updated`. |
| group-level `maven-metadata.xml` | Plugin prefixes. |

A virtual repository merges member metadata: the union of versions, the newest
`lastUpdated`, the newest snapshot. Artifactory recalculates on deploy and on demand with
`POST <base>/api/maven/calculateMetadata/<repo>/<path>[?noneRecursive=true]`.

### Snapshots and layouts

`snapshotVersionBehavior` is `unique` (default since 7.41; the file is renamed to
`<a>-<baseVersion>-<timestamp>-<buildNumber>[-<classifier>].<ext>`), `non-unique`
(`-SNAPSHOT` is kept and overwritten) or `deployer` (whatever the client sent).
`maxUniqueSnapshots` of 0 means unlimited. In 6.20 the cleanup runs every five minutes on
folders quiet for a minute and has no floor; the knowledge base article above is reported
to give an effective minimum of 2. Gradle, Ivy and SBT reuse the handler with their own
layouts; custom layouts define the patterns listed in
[03-repository-configuration-model.md](03-repository-configuration-model.md).
[Research 27](27-maven.md) has the detail: metadata at each level, snapshot naming, checksum
and POM checks, version tokens and the two REST operations.

### Unverified

The points in [research 27 section 11](27-maven.md#11-inferred-and-undocumented), among them
the `maxUniqueSnapshots` floor, the snapshot `<version>` Artifactory 7 writes, the statuses
of metadata uploads, and whether 7.x checks `.sha256` and `.sha512` uploads.

## PyPI

Upstream: the PyPA [Simple repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/)
(PEPs 503, 592, 629, 658, 691, 700 and 714), the upload API <https://docs.pypi.org/api/upload/>
and the JSON API <https://docs.pypi.org/api/json/>.
JFrog: <https://docs.jfrog.com/artifactory/docs/pypi-repositories.md>,
<https://docs.jfrog.com/artifactory/reference/calculatepypirepositorymetadata.md>.
[Research 28](28-pypi.md) confirms each claim below against 7.x with its source, and decision
[0037](decisions/0037-pypi.md) records what artifactweave serves.

### URL layout

Rooted at `<base>/api/pypi/<repo>`:

| Path | Behaviour |
|---|---|
| `GET /simple` | 302 to `/simple/`. The `Location` honours `X-JFrog-Override-Base-Url` (7.3.0), and JFrog intends it to use the custom base URL otherwise (RTFACT-31494). |
| `GET /simple/` | Project list as PEP 503 anchors. |
| `GET /simple/<normalised>/` | Files for a project. Normalisation: lower-case with runs of `-`, `_`, `.` collapsed to `-`. 7.x pages start `<!DOCTYPE html>` (7.33.6), carry `<meta name="api-version" value="2" />` rather than `pypi:repository-version`, and give each file a relative link `../../<path>#sha256=<hex>` with `rel="internal"`, `data-requires-python` (HTML-escaped) and `data-yanked`. No `data-gpg-sig`. `data-dist-info-metadata` only with the PEP 658 system property. A 7.18.3 request with an unnormalised name answered 404, not a redirect. `ETag` and 304 from 7.98.7. |
| same with `Accept: application/vnd.pypi.simple.v1+json` | PEP 691 JSON when the global "Enable simple json format" is on (7.146.7, SaaS 7.139.1); HTML otherwise. Before 7.146.7 the JSON type answered 406. The `api-version` 7.x writes is unconfirmed. |
| `GET /<path>.metadata` | PEP 658 core metadata, only with a system property that turns it on (RTFACT-31470); its name is unconfirmed. |
| `GET /<path>` | The file at `<path>` in the repository (7.x; 6.x linked `../../packages/<path>`). `Range` from 7.133.3 (SaaS 7.128.0). A `PUT` deploys there. |
| `POST <base>/api/pypi/<repo>` | Upload (twine, Poetry, uv): `multipart/form-data` with `:action=file_upload`, `protocol_version=1`, `content`, `filetype` (`bdist_wheel`, `sdist`), `pyversion`, `metadata_version`, `name`, `version`, digests (twine 7 and uv send `sha256_digest` and `blake2_256_digest`, Poetry also `md5_digest`), and the core metadata fields in snake case. Basic authentication. The root only: a folder answers 405. Answers 200. |
| `GET /pypi/<project>/json`, `GET /pypi/<project>/<ver>/json` | PyPI JSON API shape, for local repositories from 7.111.4, without `releases`, `downloads`, `has_sig`, `bugtrack_url` and `vulnerabilities`; the nine `info` keys 7.111.4 left out are served from 7.161.15. |
| `POST <base>/api/pypi/<repo>/reindex[?async=0/1]` | Reindex, local only, Manage, synchronous by default. |
| `POST <base>/api/pypi/<repo>` with XML-RPC | `pip search`, deprecated; local and virtual only. |

In Artifactory the JSON form is an opt-in global setting that is off by default. When JSON
is requested but unavailable the server falls back to HTML, and a virtual repository
serves JSON only when every member can. Reverse proxies must send
`X-JFrog-Override-Base-Url`. Errors are in the Artifactory envelope,
`{"errors":[{"status","message"}]}`.

### Storage and remote

A twine upload is stored at `<name>/<version>/<filename>`, the folder with `-` for `_`; how
7.x spells the folder for upper case and dots is unconfirmed. Files are indexed by the metadata
inside them wherever they are stored, and a REST upload must carry `pypi.name` and
`pypi.version` when 7.x cannot read the file. `packages/**` and `simple/**` are reserved in
every PyPI repository, local ones included, and deploys there answer 400. The remote's
`pyPIRegistryUrl` defaults to `https://pypi.org`; with a custom suffix the repository URL
equals the registry URL. Properties `pypi.name`, `pypi.version` and `pypi.summary` are set on
upload or on indexing, and 7.x also keeps `pypi.normalized.name`, `pypi.requires.python` and
`pypi.yanked`. Enforce layout and naming normalisation are global system properties
(`artifactory.pypi.enforce.layout`, `artifactory.pypi.enforce.naming.normalization`), from
7.70.2 by the documentation and 7.90.5 by the release notes.

### Unverified, capture against a live Artifactory

- The points in [research 28 section 15](28-pypi.md#15-inferred-and-undocumented), among them
  the PEP 658 property and its attributes, the folder for names with upper case or dots, the
  JSON index's `api-version`, and the upload's answers to bad requests.
- The remote `url` value for pypi.org is `https://files.pythonhosted.org` (confirmed, [research 17](17-undocumented-shapes.md)).

## NuGet

Upstream: <https://learn.microsoft.com/en-us/nuget/api/overview>,
<https://learn.microsoft.com/en-us/nuget/api/package-base-address-resource>,
<https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource>,
<https://learn.microsoft.com/en-us/nuget/api/search-query-service-resource>,
<https://learn.microsoft.com/en-us/nuget/api/package-publish-resource>, and the client's
V2 parser <https://source.dot.net/NuGet.Protocol/LegacyFeed/V2FeedParser.cs.html>.
JFrog: <https://docs.jfrog.com/artifactory/docs/nuget-repositories.md>,
<https://docs.jfrog.com/artifactory/reference/calculatenugetrepositorymetadata.md>.

### V3

`GET <base>/api/nuget/v3/<repo>/index.json`, or the same path without the file name, returns
`{"version":"3.0.0","resources":[{"@id","@type"}]}`. Microsoft requires four resources,
`PackageBaseAddress`, `PackagePublish`, `RegistrationsBaseUrl` and `SearchQueryService`. The
client asks for `SearchQueryService` as `/Versioned`, `/3.4.0` or `/3.0.0-beta` (`/3.5.0` for
the package type filter), and for `RegistrationsBaseUrl` as `/Versioned`, `/3.6.0` (SemVer 2,
gzip), `/3.4.0` (gzip), `/3.0.0-rc`, `/3.0.0-beta` or plain. It uses
`SymbolPackagePublish/4.9.0`, `SearchAutocompleteService` and `LegacyGallery/2.0.0` (the V2
feed) when they are listed. 7.x's registration hives are `<v3>/registration/` and
`<v3>/registration-semver2/`, its search `<v3>/query`, and its symbols push `<v3>/symbols`
([research 29 section 3](29-nuget.md#3-the-v3-service-index)).

| Resource | Paths and shape |
|---|---|
| Flat container | `{@id}/<lower-id>/index.json` with `{"versions":[...]}`, normalised and lower-case, unlisted versions included, 404 for an unknown id; `{@id}/<lower-id>/<lower-ver>/<lower-id>.<lower-ver>.nupkg`; `.../<lower-id>.nuspec`. 7.x serves it only in repositories with Enforce Layout ([research 29 section 4](29-nuget.md#4-the-flat-container-packagebaseaddress)). |
| Registration | `{@id}/<lower-id>/index.json` with `count` and `items` of pages (`@id`, `count`, `lower`, `upper`, optional `items`); a page's leaves are `{@id,catalogEntry,packageContent}`. 7.x links its pages even for one version, serves `page/<lower>/<upper>.json` for any bounds, writes no `published`, and points `packageContent` at `<hive>/Download/<id>/<version>` ([research 29 section 5](29-nuget.md#5-registration)). |
| Search | `{@id}?q=&skip=&take=&prerelease=&semVerLevel=2.0.0&packageType=` returning `{totalHits,data[{id,version,versions[{version,downloads,@id}],description,authors,tags,registration,packageTypes}]}`; unlisted packages are never returned, and SemVer 2 packages only with `semVerLevel=2.0.0`. |
| Publish | `PUT <base>/api/nuget/<repo>[/<folder>]/` with `multipart/form-data` whose first part is the `.nupkg`, header `X-NuGet-ApiKey: <user>:<password or token>` read as Basic credentials. 7.x answers 201; pushing an existing release again without Delete answers 403, while a pre-release is overwritten with Deploy alone; under Enforce Layout a duplicate answers 409. `DELETE .../<id>/<version>` is a hard delete, and 7.x has no unlisting ([research 29 section 7](29-nuget.md#7-push-delete-and-relist)). |
| Symbols | `PUT <base>/api/nuget/v3/<repo>/symbols` for `.snupkg`, stored at the root as `<id>.<version>.snupkg` ([research 29 section 8](29-nuget.md#8-symbols)). |

### V2 OData

Still served at `<base>/api/nuget/<repo>/`. The client reads the root, `$metadata` (a 404 is
tolerated), `Packages(Id='x',Version='y')` (a 404 is read as empty),
`FindPackagesById()?id='x'&semVerLevel=2.0.0` (anything but 200 or 204 is fatal),
`Search()?searchTerm='x'&targetFramework=''&includePrerelease=true[&$filter&$orderby&$skip&$top]&semVerLevel=2.0.0`
and `Packages()` with the same options. It follows `<link rel="next">` and reads the `d:`
properties listed in [research 29 section 11](29-nuget.md#11-the-v2-odata-feed). 7.x's entries
download from `<base>/api/nuget/<repo>/Download/<id>/<version>`, and `GetUpdates()` is
deprecated from 7.49.3. Remote repositories expose `feedContextPath`, `downloadContextPath` and
`v3FeedUrl`.

### Other

Per-repository "force authentication" makes Artifactory answer an anonymous NuGet request with
401, which is what makes the client send its credentials; SemVer 2 packages are hidden from
clients older than 4.3.0. A push stores `[<folder>/]<id>.<version>.nupkg`, and Enforce Layout
normalises the version and refuses duplicates
([research 29 section 10](29-nuget.md#10-storage-versions-and-enforce-layout)). Push and reindex
set the `nuget.*` properties from the nuspec, among them `nuget.id`, `nuget.version` and
`nuget.dependency` ([research 29 section 12](29-nuget.md#12-properties-and-reindex)). Errors are
answered in the Artifactory envelope, whose body the client never reads.

### Unverified

The points in [research 29 section 17](29-nuget.md#17-inferred-and-undocumented), among them
7.x's flat container, publish and autocomplete URLs, the path of a push under Enforce Layout,
the registration's page size, and how search matches terms.

## Helm

Upstream: <https://helm.sh/docs/topics/chart_repository/>. JFrog:
<https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories.md>,
<https://docs.jfrog.com/artifactory/reference/calculatehelmchartindex.md>,
<https://docs.jfrog.com/artifactory/reference/helmchartspartialreindexing.md>.

### Classic repositories

`helm repo add <name> <base>/api/helm/<repo>` fetches `GET <base>/api/helm/<repo>/index.yaml`;
chart downloads follow the `urls` entries, absolute
(`<base>/api/helm/<repo>/<chart>-<ver>.tgz`) or relative when
`artifactory.helm.relative.urls.enabled` is set (7.59.5+). Authentication is basic, with a
token as the password.

`index.yaml` has `apiVersion: v1`, `generated`, and `entries` mapping each chart name to
a newest-first list of `{name, version, appVersion, description, created, digest (sha256 of the archive), urls, home, sources, keywords, maintainers}`
plus the other Chart.yaml fields. A local repository regenerates on upload and delete. A
virtual repository merges member indexes and caches the result for
`virtualRetrievalCachePeriodSecs` (sample 600, provider default 7200, JFrog recommends at
least 60); the "six minutes" in the documentation is the merge lock timeout
(`artifactory.virtual.repo.metadata.merge.lock.timeout.sec.HELM`), not the cache period.
Charts in a virtual can be fetched per member at `/<virtual>/<member>/chart.tgz`.

Upload is `PUT <base>/<repo>/<path>/<chart>-<ver>.tgz`, optionally with a
`<chart>-<ver>.tgz.prov` provenance file beside it. With enforce layout (7.104.2+) the
file name must match the Chart.yaml name and version and duplicates answer 403. Full
reindex is `POST <base>/api/helm/<repo>/reindex`; partial reindex takes the path as a
segment (7.105.2+). Remote dependencies are rewritten under `_external/<scheme>/<host>/...`.

### OCI charts

`helm registry login`, `helm push oci://host/<repo>` and `helm pull` use the Docker path
with the `helmoci` package type (Helm 3.8+). The 7.75.3 "OCI first" default applies to
repository creation in the web UI only; `packageType: helm` over REST still creates a
classic repository. Helm and Helm OCI repositories cannot share a virtual. Promotion uses
the Docker promote endpoint.

### Unverified

Nothing in this section is unverified.

## Terraform providers and modules

Upstream: <https://developer.hashicorp.com/terraform/internals/provider-registry-protocol>,
<https://developer.hashicorp.com/terraform/internals/module-registry-protocol>,
<https://developer.hashicorp.com/terraform/internals/login-protocol>,
<https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol>,
<https://developer.hashicorp.com/terraform/language/backend/remote>. JFrog:
<https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md>,
<https://docs.jfrog.com/artifactory/reference/calculateterraformindex.md>.

### Discovery and login

`GET https://host/.well-known/terraform.json` at the host root returns absolute URLs ending
in a slash: `{"modules.v1":"https://<host>/artifactory/api/terraform/v1/modules/","login.v1":{client,grant_types:["authz_code"],authz,token},...}`,
with no `ports` in `login.v1` (a 2024 capture;
[RTFACT-27433](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27433) added the slash).
`providers.v1` is served from 7.125.4, when local repositories became provider origin
registries ([RTFACT-31167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31167)
records it missing before); its value is not captured *(unverified)*. Relative values would
resolve against the discovery document. `terraform login host` stores the token in
`credentials.tfrc.json`, and Terraform sends it, or `TF_TOKEN_<host>`, as
`Authorization: Bearer` to the host's services. artifactweave serves the document before
authentication and omits `login.v1` until
[#262](https://github.com/weaveplatform/artifactweave/issues/262)
([decision 0040](decisions/0040-terraform.md); [research 31](31-terraform.md) sections 1
and 6).

### Modules

Module sources are written `host/<repo>__<namespace>/<name>/<provider>`: the repository
key and the namespace are joined with two underscores, because the module protocol is
host-rooted and has no repository segment. Terraform allows that joined namespace 64
characters ([RTFACT-31203](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31203)).
`GET <modules.v1><repo>__<ns>/<name>/<provider>/versions`
returns `{"modules":[{"versions":[{"version":"1.0.0"}]}]}`;
`GET .../<version>/download` answers 204 with `X-Terraform-Get` naming the archive.
Terraform sends no credentials to that URL, and 7.x signs it for 90 seconds, as JFrog's
reproductions on remote repositories show; for a local repository the form is
*(unverified)*. The same operations are served repository-scoped at
`<base>/api/terraform/<repo>/v1/modules/<ns>/<name>/<provider>/...`.
Storage is `<repo>/<namespace>/<name>/<provider>/<version>.zip`, uploaded with
`PUT <base>/<repo>/<ns>/<name>/<provider>/<ver>.zip` or `jf terraform publish`, which uses
its `--tag`, often `v`-prefixed, as the version ([research 31](31-terraform.md) section 2).

### Providers

Provider sources are written `host/<repo>-rt-ns-<namespace>/<type>` (7.125.4+), and
Terraform sends the namespace in lower case.
`GET <providers.v1><repo>-rt-ns-<ns>/<type>/versions` returns
`{"versions":[{version,protocols,platforms:[{os,arch}]}]}`, where `protocols` comes from the
release's `_manifest.json` and defaults to `["5.0"]`;
`GET .../<ver>/download/<os>/<arch>` returns
`{protocols,os,arch,filename,download_url,shasums_url,shasums_signature_url,shasum,signing_keys:{gpg_public_keys:[{key_id,ascii_armor,trust_signature,source,source_url}]}}`.
The files are `terraform-provider-<type>_<ver>_<os>_<arch>.zip`,
`terraform-provider-<type>_<ver>_SHA256SUMS` and `..._SHA256SUMS.sig`, indexed by their
file names at any folder depth (JFrog's own example adds a folder level). The local
repository needs a GPG key pair, bound at creation; that the field is `primaryKeyPairRef` is
*(unverified)*. Terraform fetches the zip, `SHA256SUMS` and the signature without
credentials. The network mirror alternative lives per repository at
`<base>/api/terraform/<repo>/providers/` serving `<hostname>/<ns>/<type>/index.json` and
`<ver>.json`, with signed archive URLs
([RTFACT-29753](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29753)); which hashes
it lists, `h1:` as Terraform's own mirrors do or none, is *(unverified)*. Remote
repositories point at `https://registry.terraform.io` or `https://registry.opentofu.org`
with `https://releases.hashicorp.com` for provider files.
Reindex is `POST <base>/api/terraform/<repo>/reindex` ([research 31](31-terraform.md)
sections 3 and 4).

### State backend

The `terraformbackend` package type implements the `remote` backend form (`hostname`,
`organization = <repo-key>`, `workspaces { prefix }`), which is the Terraform Enterprise
API: a `tfe.v2` entry in the discovery document plus organisations, workspaces, state
versions and locking, with state encrypted under the master key (`encryptStates`). The
simpler HTTP backend (`address`, `lock_address`, `unlock_address`, `LOCK` and `UNLOCK`
methods, 423 and 409 on contention) is incompatible with existing `backend "remote"`
blocks. The package type is cut from version 1; see
[12-open-questions.md](12-open-questions.md).

### Unverified, capture against a live Artifactory

- The paths Artifactory uses for the `tfe.v2` service *(unverified; undocumented)*.

## Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation.

**The P6 and P7 protocols** were checked claim by claim in their own reports:
- Docker and OCI: [research 23 section 12](23-docker-oci-registry.md#12-artifactory-7x-check);
- npm: [research 25 section 11](25-npm.md#11-artifactory-7x-check);
- Go: [research 26 section 8](26-go-modules.md#8-artifactory-7x-check);
- Maven: [research 27 section 13](27-maven.md#13-artifactory-7x-check).

What they found that changes this report:
- Docker sets `docker.os` and `docker.architecture` (7.125.4).
- npm's `api/npm/auth` dropped `always-auth` and `email` (7.161.26), and copy and move into
  `.npm/` are refused (7.146.7).
- Maven accepts more POMs (7.125.4, SaaS 7.110.5), and its tokens resolve in remote caches and
  virtual repositories too.

**The P8 protocols** (PyPI, NuGet, Helm, Terraform) were checked against the release notes
only. P8's research must confirm them claim by claim. The notes change or add:
- **NuGet**, since confirmed claim by claim in
  [research 29 section 18](29-nuget.md#18-artifactory-7x-check), which also corrects this
  report's NuGet section (research 29 section 16):
  - Enforce Layout, which opens the `PackageBaseAddress` service, normalises versions and
    refuses duplicates with 409
    ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed),
    SaaS 7.139.1);
  - a newer handler, opt-in on self-managed and default on SaaS ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed));
  - strict version validation and a rate limit on search ([7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed));
  - `repository-signatures` for remote v3 repositories, and a migration tool to the new
    normalisation ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)).
- **PyPI:** confirmed claim by claim in [research 28 section 16](28-pypi.md#16-artifactory-7x-check)
  on 2026-10-19 ([#527](https://github.com/weaveplatform/artifactweave/issues/527)), with the
  release-note history from 7.84 in its section 13. The notes change or add:
  - the Simple JSON API, opt-in through a global setting and `Accept` ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed));
  - `Range` on downloads and `uv` support ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed));
  - a negative cache for local simple-index lookups, and the nine JSON API `info` keys
    ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed));
  - the JSON API in local repositories ([7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed)),
    `ETag` on package indexes ([7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed))
    and enforce layout with name normalisation ([7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed)).
- **Helm:** faster `index.yaml` generation ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)), with no protocol change.
- **Terraform:** remote repositories refuse external URLs not on an allow list, a breaking
  change ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)).
  The registry, modules and providers are checked claim by claim in
  [research 31 section 16](31-terraform.md#16-artifactory-7x-check), which corrects the
  sections above.

**Unconfirmed for 7.x:** the items under each "Unverified" heading, which stay on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
