# Docker and OCI registry

Research baseline: **2026-10-16**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#12-artifactory-7x-check) records it.

Phase P6 serves Docker and OCI images from local `docker`, `oci` and `helmoci` repositories.
[Research 04](04-package-protocols.md) sketched the protocol, [research 17](17-undocumented-shapes.md)
resolved the token challenge, and decisions [0003](decisions/0003-repository-model.md),
[0005](decisions/0005-authentication-and-tokens.md), [0015](decisions/0015-testing-and-acceptance.md),
[0018](decisions/0018-package-index.md) and [0029](decisions/0029-cdn-delivery-and-download-redirect.md)
set the direction. This report collects what the specification, Artifactory and its clients
do where those leave a point open. Decision [0031](decisions/0031-docker-oci-registry.md)
records the choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-16 |
| **[S]** | seen in source code at the cited commit |
| **[KB]** | stated in a JFrog knowledge-base article at the cited URL |
| **[J]** | recorded in JFrog's public Jira: an issue fixed in a 7.x release, or a JFrog staff comment or reproduction on it ([research README](README.md#target-version-and-sources)) |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **The OCI Distribution Specification** v1.1.1 (`spec.md` at tag `v1.1.1`), and `main` at
  `9727462` for what was added after that release.
- **The conformance suite** in the same repository, `conformance/` at tag `v1.1.1`.
- **The reference registry** `distribution/distribution` v3.0.0 (`678b095af9c9`),
  `registry/api/errcode/register.go`, for the status each error code is sent with, and
  `registry/storage/{ocimanifesthandler,schema2manifesthandler,manifestlisthandler}.go` for
  what a manifest push checks.
- **The OCI Image Specification** v1.1.1 (`descriptor.md` at `de7a13cc5ce0`).
- **JFrog's documentation:** `docker-repositories` and `additional-docker-information` under
  `https://docs.jfrog.com/artifactory/docs/` (read with `.md` appended), and
  `https://docs.jfrog.com/integrations/reference/promotedockerimage.md`.
- **JFrog's knowledge base:** "Docker login" in *How to debug Docker configuration issues*
  (`jfrog.com/help/r/artifactory-how-to-debug-docker-configuration-issues/docker-login`),
  *How to troubleshoot Docker problems* (`.../docker-login-docker.art.local`), *How to fix
  a missing signature key Docker client error* (2021), and, read on 2026-10-18, *Artifactory
  404 MANIFEST_UNKNOWN when retrieving Docker manifests from an Artifactory remote repository
  using curl*
  (<https://jfrog.com/help/r/artifactory-404-manifest-unknown-when-retrieving-docker-manifests-from-an-artifactory-remote-repository-using-curl>).
- **JFrog's public Jira:** the issues cited by key, at `https://jfrog.atlassian.net/browse/<KEY>`,
  read through `rest/api/2/issue/<KEY>` on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).
- **The cached search definitions** in `.specoracle/artifactory-searches-api/`:
  `listDockerRepositories`, `listDockerTags` and `findParentManifestLists`, and the reverse
  proxy configuration in `.specoracle/administration-jfrog-platform-rest-apis/`.
- **The JFrog CLI** `jf` 2.126.0 and the modules it pins: jfrog-cli-artifactory
  `4a0f3827cfa0` (`artifactory/commands/ocicontainer`, `artifactory/commands/container`) and
  jfrog-client-go `0646d7b50f9a` (`artifactory/services/dockerpromote.go`).
- **Docker's deprecation list**, `docs/deprecated.md` in `docker/cli` on 2026-10-16.

Artifactory's Docker support is not in the open-source edition, so the
`knottnhupfer/artifactory-oss` mirror used by research 22 holds none of it. Artifactory's own
behaviour below is documented or seen in its clients; where neither shows it, the claim is
**[I]** and the point is on the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

## 1. The protocol

### 1.1 Endpoints

- **[D]** The specification's endpoints, from its summary table:

  | ID | Method | Path | Success | Failure |
  |---|---|---|---|---|
  | end-1 | `GET` | `/v2/` | 200 | 404, 401 |
  | end-2 | `GET`, `HEAD` | `/v2/<name>/blobs/<digest>` | 200 | 404 |
  | end-3 | `GET`, `HEAD` | `/v2/<name>/manifests/<reference>` | 200 | 404 |
  | end-4a | `POST` | `/v2/<name>/blobs/uploads/` | 202 | 404 |
  | end-4b | `POST` | `/v2/<name>/blobs/uploads/?digest=<digest>` | 201, 202 | 404, 400 |
  | end-5 | `PATCH` | `/v2/<name>/blobs/uploads/<reference>` | 202 | 404, 416 |
  | end-6 | `PUT` | `/v2/<name>/blobs/uploads/<reference>?digest=<digest>` | 201 | 404, 400 |
  | end-7 | `PUT` | `/v2/<name>/manifests/<reference>` | 201 | 404, 413 |
  | end-8a | `GET` | `/v2/<name>/tags/list` | 200 | 404 |
  | end-8b | `GET` | `/v2/<name>/tags/list?n=<integer>&last=<tagname>` | 200 | 404 |
  | end-9 | `DELETE` | `/v2/<name>/manifests/<reference>` | 202 | 404, 400, 405 |
  | end-10 | `DELETE` | `/v2/<name>/blobs/<digest>` | 202 | 404, 400, 405 |
  | end-11 | `POST` | `/v2/<name>/blobs/uploads/?mount=<digest>&from=<other_name>` | 201, 202 | 404 |
  | end-12a | `GET` | `/v2/<name>/referrers/<digest>` | 200 | 404, 400 |
  | end-12b | `GET` | `/v2/<name>/referrers/<digest>?artifactType=<artifactType>` | 200 | 404, 400 |
  | end-13 | `GET` | `/v2/<name>/blobs/uploads/<reference>` | 204 | 404 |

- **[D]** Three endpoints are on `main` only: `end-4c`
  (`POST .../blobs/uploads/?digest-algorithm=<algorithm>`, for a digest other than
  `sha256`), `end-7b` (`PUT .../manifests/<digest>?tag=a&tag=b`, which answers an `OCI-Tag`
  header for each accepted tag) and `end-14` (`DELETE` on the upload location, 204, which
  cancels a session). v1.1.1, the latest release, has none of them, and its conformance
  suite tests none of them.
- **[D]** `<name>` matches
  `[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(\/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*`. A tag is at
  most 128 characters and matches `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`. A `<reference>` is a
  tag or a digest and nothing else.
- **[D]** `Docker-Content-Digest` and `Docker-Distribution-API-Version` are legacy headers
  clients should not depend on; `TAG_INVALID` and `MANIFEST_UNVERIFIED` are legacy schema-1
  codes in the same position.

### 1.2 Rules for pushes and reads

- **[D] Blob uploads.** `POST` opens a session: 202 with a `Location` holding a unique
  session id. A chunk's `Content-Range` is inclusive, the first starts at 0, and chunks must
  arrive in order or the registry answers 416. Each accepted chunk answers 202 with
  `Location` and `Range`. Closing with `PUT ?digest=` answers 201 with the blob's
  `Location`, and a `GET` on the session answers 204 with `Range` and `Location`. A registry
  that does not take single-request monolithic uploads answers 202 and the client follows
  with a `PUT`. If it has a minimum chunk size it says so with `OCI-Chunk-Min-Length`.
- **[D] Mounts.** A mount answers 201 with the blob's `Location`; a registry that cannot
  mount the blob answers 202 with a session instead.
- **[D] Manifests.** A successful push answers 201 with `Location`, and
  `Docker-Content-Digest` equal to the client's digest. Registries should accept manifests
  of at least 4 MB and answer 413 above their limit. A manifest whose blobs or child
  manifests are unknown is refused with `MANIFEST_BLOB_UNKNOWN`, except that a `subject`
  naming a manifest that does not exist must be accepted. When the manifest has a
  `mediaType`, clients set `Content-Type` to it.
- **[D] Descriptor sizes.** A descriptor's `size` is required and gives "the size, in
  bytes, of the raw content"; "if the length of the retrieved content does not match the
  specified length, the content SHOULD NOT be trusted" (image specification,
  `descriptor.md`). The distribution specification asks nothing of sizes at a push; a
  manifest that fails a registry's checks is `MANIFEST_INVALID` (code-6), and
  `SIZE_INVALID` (code-10) is for an upload whose length is wrong.
- **[S] The reference registry checks presence only.** Its OCI, schema-2 and manifest-list
  handlers `Stat` each descriptor's digest and never compare the descriptor's size with
  the stored content.
- **[D] Subject and referrers.** A registry with the referrers API answers a push carrying
  `subject` with `OCI-Subject: <subject digest>`. The referrers response is an image index
  (`application/vnd.oci.image.index.v1+json`), never 404 once supported, and empty when
  nothing matches. Each descriptor carries the manifest's `artifactType`, or for an image
  manifest without one the config's `mediaType`, and the manifest's annotations. Filtering
  on `artifactType` is a should; when applied, `OCI-Filters-Applied: artifactType` says so.
- **[D] Tags.** `tags/list` returns tags "in lexical order (i.e. case-insensitive
  alphanumeric order)", limited by `n` and continuing after `last`, with a `Link` header
  for the next page.
- **[D] Deletes.** Deleting a tag or a manifest answers 202; a registry that disables
  either answers 400 or 405. Deleting a blob answers 202, 404 when unknown, and 400 or 405
  when disabled. After a manifest delete its digest and every tag pointing at it answer 404.

### 1.3 Error codes

- **[D]** A 4xx body may be in any format; a JSON body must be
  `{"errors":[{"code","message","detail"}]}`. `code` is one of the fourteen in the table
  below, `message` is optional and `detail` is optional arbitrary JSON.
- **[S]** The reference registry sends each code with one status, which the clients have
  been written against:

  | Code | Status | Code | Status |
  |---|---|---|---|
  | `BLOB_UNKNOWN` | 404 | `NAME_INVALID` | 400 |
  | `BLOB_UPLOAD_INVALID` | 404 | `NAME_UNKNOWN` | 404 |
  | `BLOB_UPLOAD_UNKNOWN` | 404 | `SIZE_INVALID` | 400 |
  | `DIGEST_INVALID` | 400 | `UNAUTHORIZED` | 401 |
  | `MANIFEST_BLOB_UNKNOWN` | 400 | `DENIED` | 403 |
  | `MANIFEST_INVALID` | 400 | `UNSUPPORTED` | 405 |
  | `MANIFEST_UNKNOWN` | 404 | `TOOMANYREQUESTS` | 429 |

  It also defines `RANGE_INVALID` with 416, for an out-of-order chunk, outside the
  specification's list.
- **[D]** The conformance suite expects `UNSUPPORTED` when a tag delete answers 400, and a
  body with a code from the list when a malformed manifest reference
  (`sha256:totallywrong`) answers 400; 404 is also accepted there.

### 1.4 Schema 1

- **[D]** Docker deprecated pushing and pulling image manifest v2 schema 1 in 19.03,
  disabled it by default in 26.0 and removed it in 28.2, which refuses to pull such an
  image. Its replacements are schema 2 and the OCI image specification.
- **[D]** Artifactory's repository configuration has `blockPushingSchema1`, documented as
  `false` by default (research 03). A captured repository showed it `true`.

## 2. Routing and access methods

### 2.1 What Artifactory serves

- **[D]** Artifactory's own path is `<base>/artifactory/api/docker/<repo>/v2/...`. The
  `dockerReverseProxyMethod` field of the reverse proxy configuration
  (`GET /api/system/configuration/webServer`) names the access method: `SUBDOMAIN`,
  `REPOPATHPREFIX` or `PORTPERREPO`. `serverName` holds the base host and
  `serverNameExpression` its wildcard, for example `*.jfrog.com`.
- **[D]** Clients address `<host>/<repo>/<image>:<tag>` on the repository-path method and
  `docker login` takes the host alone. The credentials endpoint is
  `<base>/artifactory/api/docker/<repo>/<v1|v2>/auth`, and a context path cannot be part of
  a registry reference (`docker-repositories`).
- **[D]** Under the subdomain method a proxy maps `<repo>.<host>` to the repository, so the
  server sees `/v2/<image>/...` and takes the repository from the host. Under the ports
  method each repository has its own listener (research 04).
- **[D]** `GET <base>/artifactory/api/docker/<repo>/v2/_catalog[?n=&last=]` answers
  `{"repositories":[...]}` with the image names of one repository, and
  `GET .../v2/<image>/tags/list[?n=&last=]` answers `{"name","tags"}`. From 7.90.1 the tags
  list leaves out tags that do not follow the OCI and Docker naming rules; the switch that
  turns this off is named `...docker.filter.digests.from.tags.list.enabled`, so what it
  drops is digest-named entries. Both lists have been paginated since 5.4.6. The catalog
  needs read permission; JFrog's page for the tags list says it needs "a privileged user".
- **[D]** The catalog shows a path-scoped reader every image: "Any user with read access to a
  repository can view its metadata, which includes the list of package, image, or artifact
  names it holds. This includes users whose access is scoped to a specific path. Access to
  package content is governed separately by your permission targets"
  ([docker-repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories.md), "Repository Metadata and Permissions", updatedAt
  2026-09-21). **[J]** [RTFACT-26945](https://jfrog.atlassian.net/browse/RTFACT-26945) (affects 7.33.6, Done) had a path-scoped
  user refused with 403, and [RTFACT-30175](https://jfrog.atlassian.net/browse/RTFACT-30175) names a system property,
  `artifactory.docker.filter.catalog.by.user.permission`, whose default no source gives.
- **[D]** The `auth` endpoints answer the `config.json` entry keyed by the registry's URL,
  scheme and host only: `{"https://artprod.company.com": {"auth": "<base64>", "email":
  "admin@email.com"}}` ([additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information.md), "Set Your Docker
  Credentials Manually"). **[I]** No source says which host the key names when the base URL,
  the request's host and a repository subdomain differ, nor whether a port is kept.
- **[I]** No source shows Artifactory answering a registry-wide `GET /v2/_catalog` on the
  repository-path method: its catalog belongs to one repository, and a bare `/v2/_catalog`
  names none.
- **[KB]** JFrog's two knowledge-base walk-throughs of `docker login` disagree on one point.
  Both show an unauthenticated `GET .../v2/` answering 401, then a token `GET` answering 200
  (one with the repository `null`), then the authenticated probe answering 200. One says
  the first 401 is always seen; the other says the 401 is expected when anonymous access is
  off, and that a client which receives 200 "won't use credentials".

### 2.2 Parsing a registry path

- **[I]** On the repository-path method the first segment after `/v2/` is always the
  repository key and the rest is `<name>`, so no image name can be mistaken for a key. The
  two registry-wide paths, `/v2/token` and `/v2/_catalog`, have one segment, and every
  repository operation has at least three (`<repo>/<name>/<kind>`), so a repository called
  `token` stays addressable.
- **[I]** `<name>` may contain `blobs`, `manifests`, `tags`, `referrers` and `uploads` as
  components (`team/manifests/app` is valid). Matching the operation from the end of the
  path is unambiguous: a reference never contains `/`, a digest always contains `:`, and
  `uploads` cannot be a digest.
- **[I]** `_catalog` cannot be a name component (the grammar forbids a leading `_`), so
  `/v2/<repo>/_catalog` cannot be a repository operation either.
- **[D]** Docker clients refuse upper-case letters in a reference, and a Docker, OCI or
  Helm OCI key "cannot contain uppercase letters or an underscore"
  ([repository management overview](https://docs.jfrog.com/artifactory/docs/repository-management-overview.md);
  [research 03](03-repository-configuration-model.md#key-rules)), so a key reaches the
  server as it was created. Artifactory keys are unique without regard to case (decision
  0003), so the lookup also matches a key segment a client wrote in another case.
- **[I]** A key that is still not a valid name component (a leading `-` or `.`, a trailing
  separator, a non-ASCII letter) cannot be sent by a client that checks references.
  Artifactory accepts such keys.
- **[I]** On a layout of tag folders, the tag `app` of the image `team` and the image
  `team/app` share the folder `team/app/`. Both can exist: the folder is a tag because it
  holds a manifest file, and an image because a child of it does.

### 2.3 Remote and virtual repositories

- **[D]** A virtual Docker repository resolves by member order and pushes to
  `defaultDeploymentRepo`. A remote to Docker Hub needs `enableTokenAuthentication`, and
  `externalDependenciesEnabled` allows foreign layers (research 04).
- **[S]** jf reads a remote's content from `<key>-cache` and looks under `library/` for
  single-segment Docker Hub names (`ocicontainer/docker_utils.go`). For a virtual
  repository it reads `/api/repositories/<key>` and keeps the layers in its
  `defaultDeploymentRepo` (`ocicontainer/buildinfo.go`).
- **[I]** Upstream fetching arrives with remote repositories and member resolution with
  virtual ones. Until then a remote Docker repository has nothing in its cache to serve,
  and a virtual one has neither the tag union nor `resolveDockerTagsByTimestamp`
  (decision 0008).

## 3. Authentication and tokens

- **[D]** The challenge, `service`, `scope`, the `GET`-only token endpoint with the
  repository `null` on the bare probe, and the `{"token","expires_in":3600}` body are in
  research 17 and were confirmed there. The realm is only likely there: the captures that
  show `<base>/artifactory/api/docker/<repo>/v2/token` date from 2017 to 2019.
- **[J]** On the repository-path method, a 7.71.5 capture by JFrog support shows
  `GET /v2/test-docker-local/oras-test/tags/list` challenged with
  `Bearer realm="http://artifactory.com/v2/token",service="artifactory.com",scope="repository:oras-test:pull"`:
  the realm is the bare `/v2/token`, and the scope names the image without the repository
  key, so oras fetches a new token for every request
  ([RTFACT-30264](https://jfrog.atlassian.net/browse/RTFACT-30264), reproduced on 7.71.11
  and closed without a fix). JFrog's documented proxy rewrite sends `/v2/token` to
  `/artifactory/api/docker/null/v2/token`
  ([nginx ingress install](https://docs.jfrog.com/installation/docs/install-jfrog-platform-with-nginx-ingress-controller)).
- **[KB]** JFrog's walk-through says "The Docker client gets an access token" from the
  token endpoint, with `expires_in` 3600.
- **[D]** Artifactory does not store an access token whose expiry is under the Access
  persistence threshold, 10800 seconds by default (`docs/resources/scoped_token.md` in
  `jfrog/terraform-provider-artifactory` at `990e8dfdb64e`). **[I]** A one-hour registry
  token is therefore never stored.
- **[I]** A reported 600-second lifetime for registry tokens obtained with an access token
  is not confirmed by any source read here.
- **[D]** The Docker token scope is `repository:<name>:<actions>` with the actions `pull`,
  `push`, `delete` and `*` (Docker's registry token specification, research 04). On the repository-path method the client's `<name>` includes the repository
  key. 7.71.5 leaves the key out of the challenge's scope (RTFACT-30264, above).
- **[I]** A registry token is useful only for the registry. Nothing in the clients sends
  it anywhere else.

## 4. Permissions

- **[D]** Artifactory's actions are Read, Annotate, Deploy/Cache, Delete/Overwrite and
  Manage (decision 0006).
- **[D]** "Strict Tag Overwrite", on by default and to become permanent by the end of 2026,
  requires Delete/Overwrite to push an existing name and tag (`additional-docker-information`).
- **[D]** Promotion "Requires a privileged user", and JFrog's page for the tags list says
  the same; the catalog needs Read.
- **[I]** The rest follows the item model: pulls, tag lists, referrers and the catalog need
  Read on the image's folder, pushes need Deploy, deletes need Delete, and a mount needs Read
  on the source and Deploy on the target. The properties Artifactory sets on a manifest it
  sets itself, so pushing needs no Annotate.
- **[D]** Anonymous pulls work when anonymous access is on and the anonymous user can read
  (research 04, research 17).

## 5. Storage layout

### 5.1 Files

- **[D]** A tag is a folder: `<repo>/<image>/<tag>/manifest.json`, with the config and
  layers beside it as `sha256__<hex>` files (research 04). Upload sessions and finished
  uploads live under `<image>/_uploads/` (research 04).
- **[S]** jf's searches confirm the names. It reads `manifest.json`, `list.manifest.json`
  and `sha256__<hex>` in the tag folder, and excludes `<image>/_uploads` "which are copied to
  their final location" (`ocicontainer/remoteagent.go`, `ocicontainer/docker_utils.go`).
- **[S] A manifest pushed by digest** lives in the folder `<image>/sha256:<hex>/` of a local
  repository, with a colon, and the config and layers in it keep their `sha256__<hex>`
  names. `sha256__<hex>` folders are the remote-cache form. jfrog-cli-artifactory says
  "multi-platform images are stored in local under folders like sha256:xyz format but in
  remote it's stored in folders like library/sha256__xyz format"
  (`ocicontainer/docker_dependencies.go`, `applyRepoTypeModifications`). In
  `ocicontainer/docker_utils.go`, `sha256__` is `sha256RemoteFormat`, which
  `modifyPathForRemoteRepo` applies to remote repositories only. **[J]** JFrog's issue on
  multi-architecture deletes lists the same local layout,
  `repo/image/sha256:image1/manifest.json` beside `repo/image/sha256:image1/sha256__img1layer1`
  ([RTFACT-30432](https://jfrog.atlassian.net/browse/RTFACT-30432), Done). artifactweave
  stores them in `<image>/sha256:<hex>/` too (decision 0031,
  [#387](https://github.com/weaveplatform/artifactweave/issues/387)).
- **[S]** `jf rt build-docker-create` falls back to `<image>/sha256__<hex>/*` when its tag
  search finds nothing (`ocicontainer/remoteagent.go`, `searchImage`). That is the
  remote-cache form, so in a local repository the fallback finds no image pushed by digest
  alone, on 7.x as here.
- **[S]** For a multi-architecture tag jf reads `<image>/<tag>/list.manifest.json`, then
  searches every folder under `<image>/` for a `manifest.json` and keys each by its
  `sha256`, which it matches against the list's entries. The platform manifests therefore
  live in folders of their own under the image, which jf finds by digest
  (`ocicontainer/buildinfo.go`, `performMultiPlatformImageSearch`).
- **[S] `jf docker build` and `jf docker buildx build`** run a search of their own. They read
  the platform digests from the list and search the exact paths `<image>/sha256:<hex>` with
  `$eq` (`ocicontainer/docker_manifest.go`, `createSearchablePathForDockerManifestContents`;
  `ocicontainer/docker_utils.go`, `searchArtifactoryForFilesByPath`), adding `library/` and
  `sha256__` only for a remote repository. A platform manifest in a `sha256__<hex>` folder of
  a local repository is therefore not found, and the build-info lacks its layers.
- **[S]** jf takes the image's config digest from `manifest.json` and expects the config as
  `sha256__<config hex>` in the same folder, and takes a manifest's digest from the file's
  `sha256`. The stored `manifest.json` must therefore be the exact bytes the client pushed.
- **[D]** From 7.81 the user interface shows the platform manifests nested under the tag
  folder of a list manifest. From 7.90.5 (SaaS 7.90.1) referrers are tracked in a
  `referrers.json` in the image folder. Neither is read by jf.
- **[D] Descriptive platform tags.** "Starting from Artifactory version 7.21.2, when you push a
  platform-specific Docker image, the server automatically adds a second descriptive tag
  that includes the operating system" ([docker-repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories.md),
  "Multi-Architecture Tagging", updatedAt 2026-09-21). jf reads none of them.
  - **[J]** The tag is the list's tag with the platform's os and architecture: after a list
    tagged `1.33` and `latest` naming `linux/amd64` and `linux/s390x`, the tags list held
    `1.33-linux-amd64`, `latest-linux-amd64` and `latest-linux-s390x`
    ([RTFACT-26257](https://jfrog.atlassian.net/browse/RTFACT-26257)). So it is made when the list is pushed, from the index's
    platform fields.
  - **[J]** Each is a tag folder holding the platform's `manifest.json` and its
    `sha256__<hex>` blobs; a list pushed over a single-platform tag moved that tag's manifest
    into it ([RTFACT-26810](https://jfrog.atlassian.net/browse/RTFACT-26810)).
  - **[J]** buildx lists attestation manifests under `unknown/unknown`, and a promotion
    named one `latest-attest-unknown-unknown` ([RTFACT-29773](https://jfrog.atlassian.net/browse/RTFACT-29773)). 7.111.4 then
    "Fixed an issue whereby the Promote Docker Image API renamed sub-manifest tags according
    to their architectures" (RTDEV-55450, [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed)),
    so a promotion writes none.
  - **[J]** [RTFACT-27249](https://jfrog.atlassian.net/browse/RTFACT-27249) (affects 7.39.0, deferred) and
    [RTFACT-26859](https://jfrog.atlassian.net/browse/RTFACT-26859) (affects 7.31.0, open) report that 7.x no longer adds them
    to buildx pushes.
  - **[I]** No source shows a name for an entry with a variant (`arm/v7`), which entry holds a
    name two entries give, whether Max Unique Tags counts the tags, or a switch to turn them
    off. artifactweave adds them as decision 0031 records.

### 5.2 Properties

- **[KB]** A knowledge-base article lists the properties a `manifest.json` must carry, and
  says a missing or wrong one makes the Docker client fail with "missing signature key":
  - `docker.manifest`: the tag, the folder's name;
  - `docker.manifest.digest`: `sha256:<hex of manifest.json>`;
  - `docker.manifest.type`: the media type, usually `application/vnd.docker.distribution.manifest.v2+json`;
  - `docker.repoName`: the image name;
  - `sha256`: the hex digest of `manifest.json`.
- **[S]** jf finds a manifest by digest with the AQL
  `items.find({"repo","name":"manifest.json","@sha256":"<hex>","@docker.manifest.digest":"sha256:<hex>"})`,
  so both are properties, not only checksum fields, and it warns when
  `docker.manifest.digest` differs from the local image's digest.
- **[D]** Artifactory extracts an image's labels into properties on `manifest.json`
  (`additional-docker-information`). **[I]** The property name is `docker.label.<label>`; no
  source read here shows the prefix.
- **[D]** From [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed),
  `docker.os` and `docker.architecture`, the image's operating system and architecture, are
  set on `manifest.json` "after pushing or caching a new image". **[I]** Whether a platform
  `variant` is part of `docker.architecture`, and whether `list.manifest.json` gets either
  property: the release note names only `manifest.json`, and no other source describes them.
- **[D]** Schema-1 images carried `docker.imageId`, `docker.size`, `docker.tag.name`,
  `docker.tag.content` and `docker.repoName` under a V1 layout (`.images`, `repositories`).

### 5.3 Deleting and retention

- **[D]** "the Docker client does not support DELETE commands"; images are deleted through
  the REST API with `DELETE <base>/artifactory/<repo>/<image>[/<tag>]`, which also removes
  layers no other tag uses. Empty folders are pruned every five minutes.
- **[D]** From 7.91.0 `DELETE <base>/artifactory/api/docker/<repo>/v2/delete` with
  `{"dockerRepository","tag"}` deletes a multi-architecture tag and its platform
  manifests. Before 7.118.0 that also deleted manifests and layers other tags shared; from
  7.118.0 "shared manifests and layers are protected from deletion". Research 02 lists it
  with 200.
- **[D] Max Unique Tags** (`maxUniqueTags`) defaults to 0, no limit. When a push takes an
  image over the limit, the oldest tag is removed, first in first out; `latest` is not
  exempt; only the pushed image is checked.
- **[D] Tag Retention** (`dockerTagRetention`) defaults to 1, keeping no overwritten
  versions. From 2 it keeps that many minus one previous versions of a tag, "renamed by
  their SHA2 hash", first in first out. Removing a tag through Max Unique Tags removes its
  retained versions. Retention removes a list manifest with its platform manifests as one
  unit, for tags pushed through the current upload service. Removed tags go to the trash
  can when it is on.
- **[D]** Overwriting a list manifest with a list overwrites only the list, "(default
  behavior)", unless `artifactory.docker.complete.manifest.override.enabled` is set
  ([additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information.md), "Tag Retention Logic"). From
  [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) (SaaS 7.125.3) the Package Settings
  option "Complete list manifest image overwrite" does the same. It is global, the
  descriptor's `<repositoriesGeneralConfig><docker><completeListManifestOverwrite>`, false by
  default: "Once saved, this setting will apply to all Docker, OCI, and Helm OCI
  local/federated repositories"
  ([configuration XSD](https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd.md));
  a sibling `explicitConfig` lets the system property decide until the option is saved.
  Neither the Terraform provider nor jfrog-client-go sets it. artifactweave offers neither
  (DV-084).
- **[D]** Overwriting a list with a single manifest removes "all of the sub-manifests related
  to the original revision", and under Tag Retention "the complete structure of the previous
  revision is saved" ([additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information.md), "Overwriting List
  Manifests with a Manifest and Vice-Versa"; [RTFACT-29681](https://jfrog.atlassian.net/browse/RTFACT-29681)). artifactweave does
  both (decision 0031).
- **[D]** From 7.117.1 a promotion that overwrites a tag leaves layers other tags share.
- **[I]** No source shows what a `DELETE /v2/<name>/blobs/<digest>` or
  `DELETE /v2/<name>/manifests/<reference>` answers on Artifactory.

## 6. What the JFrog CLI reads

- **[S] `jf docker push` and `jf docker pull`** run the `docker` client, then build the
  build-info from the stored image: `GET /api/repositories/<key>`, AQL over `items` with
  `$match` and `$nmatch` on `path` (excluding `<image>/_uploads`) and the property search
  in section 5.2, a download of `manifest.json` or `list.manifest.json` and the config
  blob, and `PUT /api/storage` properties (`build.name`, `build.number`, `build.timestamp`)
  on every layer. Paths tried: `<image>/<tag>/*`, then without the first segment, then
  under `library/`, then the digest folder.
- **[S] The repository key comes from a header.** After the docker client finishes, `jf docker push` and
  `jf docker pull` send `HEAD <registry>/v2/<image>/manifests/<tag>` with the server's credentials and read
  the repository key from the `X-Artifactory-Docker-Registry` response header; without it they fail with
  "couldn't find 'X-Artifactory-Docker-Registry' header" (jfrog-cli-artifactory `4a0f3827cfa0`,
  `artifactory/commands/ocicontainer/image.go`, `GetRemoteRepo`). The P6 exit checks (#229) found this.
- **[S] `jf rt build-docker-create`** records an image pushed by another tool (kaniko,
  buildx) from a file of `<image>:<tag>@sha256:<digest>` lines, by the same search.
- **[S] `jf rt docker-promote`** sends `POST api/docker/<source>/v2/promote` with
  `{targetRepo, dockerRepository, targetDockerRepository, tag, targetTag, copy}` and
  expects 200 (`dockerpromote.go`).
- **[S]** `jf docker scan` calls Xray, which answers from a stub (research 10). `jf docker
  build` runs `docker buildx` and then its own exact-path search of `<image>/sha256:<hex>`
  (section 5.1), not the push search.

## 7. JFrog REST operations for Docker

- **[D] Promote** (`promoteDockerImage`, since 3.7): `targetRepo` and `dockerRepository`
  required; `targetDockerRepository` and `targetTag` default to the source's; `tag` omitted
  promotes every tag of the image (since 4.10); `copy` false moves. Answers 200 with plain
  text, 400 for a malformed body, 404 for an unknown repository or image. Local and
  federated sources; Helm OCI images too.
- **[D] A move without Delete.** From [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed)
  (SaaS 7.110.1, RTDEV-54091), a promotion with `copy:false` by a caller without Delete on
  the source copies the image, leaves the source in place and answers 206, "indicating
  partial success". **[S]** `jf rt docker-promote` accepts 200 only
  (`CheckResponseStatusWithBody(resp, body, http.StatusOK)` in `dockerpromote.go`), so it
  fails on that 206 as it does on a 403. artifactweave refuses such a move with 403 before
  anything is copied (maintainer decision, 2026-10-18), a deviation registered by
  [#378](https://github.com/weaveplatform/artifactweave/issues/378).
- **[D] Parent manifests** (`findParentManifestLists`, [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed),
  SaaS 7.119.0): `POST
  .../v2/parentManifests` with `{dockerRepository, tag}` answers
  `{"parentManifestLists":[{"dockerRepository","digest","tags"}]}`; Read on the repository.
- **[D] Catalog and tags list:** section 2.1. **Multi-architecture delete:** section 5.3.

## 8. Tag retention and the package index

- **[I]** For decision 0018 an image is a package (`<image>` in its repository), a tag is a
  version, and the files of the tag folder are the version's files. The order Max Unique
  Tags needs is push time, which is the "manifest timestamps for Docker tags" that
  decision 0018 already names.
- **[D]** A digest-only folder is not a tag. In a local repository it is `sha256:<hex>`
  (section 5.1), which is not a valid tag name, so the 7.90.1 filter keeps it out of the
  tags list ([listdockertags](https://docs.jfrog.com/artifactory/reference/listdockertags.md)).
  A `sha256__<hex>` name is a valid tag and would pass the filter. **[J]** Before the
  filter, a 7.71.5 tags list showed such a folder as the tag `sha256:8947a6ac…`
  ([RTFACT-30264](https://jfrog.atlassian.net/browse/RTFACT-30264)). **[I]** Max Unique
  Tags counts tags, so digest folders do not count towards it.
- **[I]** Where Artifactory puts a version kept by Tag Retention ("renamed by their SHA2
  hash") is not shown by any source. If it follows the digest folders of section 5.1, the
  folder is `sha256:<hex>`, which is where artifactweave keeps it (decision 0031).

## 9. The conformance suite at v1.1.1

- **[D]** Four categories, each switched on by its variable: Pull (`OCI_TEST_PULL`), Push
  (`OCI_TEST_PUSH`), Content Discovery (`OCI_TEST_CONTENT_DISCOVERY`) and Content Management
  (`OCI_TEST_CONTENT_MANAGEMENT`). Content Management depends on Push and Content Discovery.
- **[D]** It reads `OCI_ROOT_URL`, `OCI_NAMESPACE`, `OCI_CROSSMOUNT_NAMESPACE`,
  `OCI_USERNAME`, `OCI_PASSWORD`, `OCI_AUTH_SCOPE`, `OCI_AUTOMATIC_CROSSMOUNT`,
  `OCI_SKIP_EMPTY_LAYER_PUSH_TEST`, `OCI_DELETE_MANIFEST_BEFORE_BLOBS` (default 1),
  `OCI_HIDE_SKIPPED_WORKFLOWS`, `OCI_DEBUG`, `OCI_REPORT_DIR`, and for pull-only runs
  `OCI_MANIFEST_DIGEST`, `OCI_TAG_NAME`, `OCI_BLOB_DIGEST` and `OCI_TAG_LIST`.
- **[S]** What it accepts where the specification gives a choice:
  - a blob delete may answer 202, 404 or 405; on 405 the "deleted blob answers 404" check is
    skipped, and every teardown accepts 405;
  - a tag delete may answer 202, or 400 or 405 when disabled;
  - a manifest with no layers must be accepted unless `OCI_SKIP_EMPTY_LAYER_PUSH_TEST` is
    set;
  - a mount without `from` answers 201 when `OCI_AUTOMATIC_CROSSMOUNT=1`, 202 when it is 0,
    and is not tested when unset;
  - a referrers request for an unknown digest answers 200 with an empty index; a filtered
    request answers two descriptors with `OCI-Filters-Applied`, or all five with a warning.
- **[D]** It is a Ginkgo module in `conformance/`, built with Go and run against the
  registry's URL; it writes an HTML and JUnit report.

## 10. Inferred and undocumented

These go to the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):

1. What `GET /v2/` answers an anonymous caller when anonymous access is on: 401 with the
   challenge, or 200 (section 2.1).
2. What `DELETE /v2/<name>/blobs/<digest>` and `DELETE /v2/<name>/manifests/<tag|digest>`
   answer (section 5.3).
3. Whether a schema-1 push is accepted when `blockPushingSchema1` is false, and what is
   answered when it is true (section 1.4).
4. Whether a registry-wide `GET /v2/_catalog` answers on the repository-path method (section 2.1).
5. The label property prefix (section 5.2) and the properties on `list.manifest.json`.
6. The folder and name of a version kept by Tag Retention (section 8).
7. The format of `referrers.json` (section 5.1).
8. The lifetime of a registry token obtained with an access token (section 3).
9. Where an uploaded blob stays under `_uploads/` after a manifest references it, and when
   it is removed.
10. The error envelope and status for a push to a remote, and for a registry request to a
    repository of another package type.
11. Which secret the `auth` endpoints put in the `config.json` entry for a caller who
    authenticated with a token.
12. Whether a manifest whose descriptor `size` differs from the stored blob or manifest is
    refused, and with which code (section 1.2).
13. Whether `docker.architecture` includes a platform `variant`, and whether
    `list.manifest.json` carries `docker.os` or `docker.architecture` (section 5.2).
14. Whether 7.161.32 still adds descriptive platform tags to a buildx push (RTFACT-27249,
    RTFACT-26859), how it names an entry with a variant, which entry holds a name two entries
    give, and whether Max Unique Tags counts the tags (section 5.1).
15. The default of `artifactory.docker.filter.catalog.by.user.permission` (section 2.1).
16. Which host the `auth` endpoints key their entry by when the base URL, the request's host
    and a repository subdomain differ, and whether a port is kept (section 2.1).
17. On the repository-path method, whether `MANIFEST_UNKNOWN`'s `detail.manifest` begins with
    the repository key (section 12).

## 11. Versions at the research baseline

| Component | Version |
|---|---|
| OCI distribution-spec | v1.1.1 (`spec.md`, `conformance/`); `main` at `9727462` |
| distribution/distribution | v3.0.0 (`678b095af9c9`) |
| jf | 2.126.0 |
| jfrog-cli-artifactory | `4a0f3827cfa0` |
| jfrog-client-go | `0646d7b50f9a` |
| Docker Engine deprecations | `docker/cli` `docs/deprecated.md`, 2026-10-16 |

## 12. Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** The audit of 2026-10-18 added JFrog's KB articles, its public Jira and the JFrog clients' sources, under the rules of the [research README](README.md#target-version-and-sources). The check of 2026-10-17 used JFrog's current documentation, read on that day (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation. This report was already written from the 7.x pages ([docker-repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories.md),
[additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information.md), [oci-repositories](https://docs.jfrog.com/artifactory/docs/oci-repositories.md), [listdockertags](https://docs.jfrog.com/artifactory/reference/listdockertags.md),
[listdockerrepositories](https://docs.jfrog.com/artifactory/reference/listdockerrepositories.md), [findparentmanifestlists](https://docs.jfrog.com/artifactory/reference/findparentmanifestlists.md)), read again on 2026-10-17.

**Confirmed for 7.x:**
- Enforce Strict Tag Overwrite: a configuration setting from [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), on by default and
  to become permanent at the end of 2026 (DV-035).
- Complete list-manifest overwrite: the behaviour from [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed) (SaaS 7.107.1), and the
  Package Settings option that turns it on from [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) (SaaS 7.125.3).
- The referrers API from [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed) (SaaS 7.90.1), and filtered tag lists.
- `parentManifests` from [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) (SaaS 7.119.0, which the API page gives as "since
  7.119.0"), needing Read on the repository.
- Shared layers kept when a promotion overwrites a tag ([7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed)).
- A publisher without Delete may not overwrite layer information (CVE-2026-68754, fixed in
  [7.161.16](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716116-self-managed)): decision 0031 asks Deploy and Delete to replace a tag.
- Digest folders: a manifest pushed by digest, every platform manifest of a
  multi-architecture push among them, lives in `<image>/sha256:<hex>/` in a local
  repository, with its blobs as `sha256__<hex>`, and artifactweave stores it there, so a
  `sha256__` name is an ordinary tag (section 5.1; jfrog-cli-artifactory
  `docker_dependencies.go`; [RTFACT-30432](https://jfrog.atlassian.net/browse/RTFACT-30432);
  [#387](https://github.com/weaveplatform/artifactweave/issues/387)).

**Changed or added in 7.x:**
- **`docker.os` and `docker.architecture`.** From [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) these properties are set on a
  pushed or cached `manifest.json`. artifactweave sets them on every pushed image manifest
  ([#352](https://github.com/weaveplatform/artifactweave/issues/352), decision 0031 "Platform properties").
- **Referrers need Read.** From [7.161.16](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716116-self-managed) (SaaS 7.164.0) the referrers API answers 403
  to a caller without Read on the subject image. Decision 0031 already requires Read on
  `<name>/` and answers `DENIED` 403.
- **Digest mismatches.** [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed) added a flag, off by default, that blocks a blob
  upload whose digest does not match its SHA-256. Without it Artifactory accepts the
  mismatch. artifactweave always refuses one with `DIGEST_INVALID`, as the distribution
  specification requires (DV-061).
- **Tag download statistics.** Optional download statistics shared across tags of one digest
  ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)).
- **`library/` in tag lists.** Tag lists with or without `library/` for official images
  ([7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)); that concerns remote caches of Docker Hub (P9).
- **Range uploads.** A fix to the start offset of range uploads ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)).
- **Platform tags.** From 7.21.2 each platform of a list push also gets a descriptive
  `<tag>-<os>-<arch>` tag, and an attestation `<tag>-unknown-unknown`; artifactweave adds
  them (section 5.1; decision 0031, "Platform tags").
- **A list overwritten by an image** removes the old list's platform manifests, and under
  Tag Retention keeps the previous revision whole; artifactweave does the same (section 5.3).
- **Complete list-manifest overwrite.** The global Package Settings option from 7.125.4, off
  by default, is not offered (section 5.3, DV-084).
- **`auth` endpoints** answer the `config.json` entry keyed by the registry URL,
  `{"https://<host>": {"auth", "email"}}`, as artifactweave does with its base URL's scheme
  and host (section 2.1).
- **Catalog visibility.** A user with Read on any path of a repository sees every image name
  in its `_catalog`, as in artifactweave (section 2.1).
- **Error texts.** `MANIFEST_UNKNOWN` carries "The named manifest is not known to the
  registry." with `{"manifest":"<image>"}` as `detail`, the image without the tag or digest,
  and `MANIFEST_INVALID` carries "manifest invalid" with `{"description":…}`, as in
  artifactweave ([KB, 7.146.10](https://jfrog.com/help/r/artifactory-404-manifest-unknown-when-retrieving-docker-manifests-from-an-artifactory-remote-repository-using-curl);
  [RTFACT-25069](https://jfrog.atlassian.net/browse/RTFACT-25069), [RTFACT-30468](https://jfrog.atlassian.net/browse/RTFACT-30468),
  [RTFACT-29656](https://jfrog.atlassian.net/browse/RTFACT-29656)).
- **Manifest redirects.** A manifest at or above the minimum direct download size redirects
  like a layer ([RTFACT-29705](https://jfrog.atlassian.net/browse/RTFACT-29705)), as in artifactweave (decision 0031, "Redirects";
  [research 19](19-cdn-and-download-redirect.md)).

**Differs in 7.x** (the audit of 2026-10-18, [#378](https://github.com/weaveplatform/artifactweave/issues/378)):
- **A move without Delete.** 7.x differs: a promotion with `copy:false` by a caller without
  Delete on the source copies the image and answers 206, where artifactweave answers 403
  (section 7; [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed), RTDEV-54091); a deviation registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).
- **Token body.** 7.x differs: the token endpoint answers `{"token","expires_in"}`, without
  the `access_token` and `issued_at` artifactweave adds
  ([RTFACT-25069](https://jfrog.atlassian.net/browse/RTFACT-25069),
  [RTFACT-26227](https://jfrog.atlassian.net/browse/RTFACT-26227), the docker.art.local
  walk-through in the knowledge base); a deviation registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).
- **Manifest `Accept`.** 7.x differs: a stored OCI manifest answers 404 `MANIFEST_UNKNOWN`
  unless `Accept` names the OCI type or the user agent is in
  `artifactory.oci.accepted.user.agents.list`, and a stored schema-2 manifest is converted
  to schema 1 for a client that sends no `Accept` or asks for schema 1; artifactweave
  serves the stored type and never converts
  ([RTFACT-25069](https://jfrog.atlassian.net/browse/RTFACT-25069), fixed in 7.18.0;
  [RTFACT-20795](https://jfrog.atlassian.net/browse/RTFACT-20795), JFrog comment;
  [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) RTDEV-63511); a deviation registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).
- **Docker V1.** 7.x differs: self-managed 7.x still documents Docker V1 repositories ("not
  supported on Cloud instances"), and neither the deprecations nor the end-of-life page
  removes them, where artifactweave serves only `.../v1/auth`
  ([additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information#use-docker-v1),
  "Use Docker V1"); recorded as a cut in [research 10](10-scope-and-tiers.md#product-families).
- **Challenge on the repository-path method.** 7.x differs: 7.71.5 challenges with
  `realm="<host>/v2/token"` and a scope that names the image without the repository key,
  where artifactweave names the repository's realm and keeps the key in the scope
  (section 3; [RTFACT-30264](https://jfrog.atlassian.net/browse/RTFACT-30264)); a deviation registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).

**Unconfirmed for 7.x:** the **[I]** items of section 10 stay on [#216](https://github.com/weaveplatform/artifactweave/issues/216). The **[S]** claims
here come from the JFrog CLI and the Docker and OCI sources, not from 6.20.
