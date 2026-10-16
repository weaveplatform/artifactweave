# 0031: Docker and OCI registry protocol and storage layout

Status: Proposed

## Context

Docker, `crane`, `oras`, `cosign` and Helm's OCI support speak the OCI Distribution
Specification, which grew out of the Docker Registry HTTP API V2. Artifactory serves it for
its `docker`, `oci` and `helmoci` repositories and stores each image as files in the
repository tree, which the JFrog CLI then reads with AQL and the storage API. Decision
[0003](0003-repository-model.md) fixes how a request reaches a repository and decision
[0005](0005-authentication-and-tokens.md) the registry token; this decision fixes what the
registry answers and how an image is stored. [Research 23](../23-docker-oci-registry.md)
holds the facts, and its section numbers are cited below as R23.

## Decision

### Packages and protocol version

The registry's HTTP layer is `internal/server/docker`: the routes of decision 0003, the
token challenge, the protocol handlers and the error envelope, mounted before the generated
servers (decision [0002](0002-spec-first-flat-crud-handwritten-item-paths.md)). The storage
layout, manifest parsing and validation, the properties set on manifests and the
package-index hooks are `internal/format/docker`, which never imports `internal/server`.
Upload sessions and the items they finish are `internal/service/registry`: they store items
through the item service, which imports `internal/format`, so a format package cannot hold
them without an import cycle. One code path serves the `docker`, `oci` and `helmoci`
package types.

The target is OCI Distribution Specification **v1.1.1**, the latest release, and its
conformance suite at the same tag. `end-4c`, `end-7b` and `end-14`, which exist only on the
specification's `main`, are served as below without being claimed.

### Operations and errors

A registry path is matched from its end. After the repository, the last segments are one
of `manifests/<reference>`, `blobs/<digest>`, `blobs/uploads/`, `blobs/uploads/<id>`,
`tags/list` or `referrers/<digest>`, and everything before them is `<name>`:
- `<name>` must match the specification's grammar, or the answer is 400 `NAME_INVALID`.
- A path that is no registry operation answers 404 with `UNSUPPORTED`.
- Every response carries `Docker-Distribution-API-Version: registry/2.0`.

Every registry error uses the specification's envelope,
`{"errors":[{"code","message","detail"}]}` with `Content-Type: application/json`, and sends
each code with the reference registry's status (R23 1.3):

| Code | Status | Code | Status |
|---|---|---|---|
| `BLOB_UNKNOWN` | 404 | `NAME_INVALID` | 400 |
| `BLOB_UPLOAD_INVALID` | 404, or 416 for an out-of-order chunk | `NAME_UNKNOWN` | 404 |
| `BLOB_UPLOAD_UNKNOWN` | 404 | `SIZE_INVALID` | 400, or 413 for a manifest over the limit |
| `DIGEST_INVALID` | 400 | `UNAUTHORIZED` | 401, with the challenge |
| `MANIFEST_BLOB_UNKNOWN` | 400 | `DENIED` | 403 |
| `MANIFEST_INVALID` | 400 | `UNSUPPORTED` | 405, or 404 for an unknown operation |
| `MANIFEST_UNKNOWN` | 404 | `TOOMANYREQUESTS` | 429 |

Only these fourteen codes are sent, with one exception, `TAG_INVALID` for the reserved
tags below; the other legacy schema-1 codes and the reference registry's `RANGE_INVALID` are
not. `message` is a short English sentence, and `detail`, when present,
names what failed (the digest, the tag, the expected and actual sizes). The JFrog REST
operations under `/artifactory/api/docker/<repo>/v2/` that are not registry operations
(`promote`, `parentManifests`, `delete`) keep the Artifactory envelope.

### Authentication on the registry

- A request with no credentials to any registry path answers 401 `UNAUTHORIZED` with the
  challenge of decision 0005, **including `GET /v2/` when anonymous access is on**. An
  anonymous client then fetches an anonymous token and pulls; a client with credentials
  learns at once that it must send them. Valid Basic or Bearer credentials on `GET /v2/`
  answer 200; invalid ones answer 401.
- On every repository operation the effective actions are those the token grants,
  intersected with what the caller's permissions allow at the time of the request, so a
  removed permission takes effect without waiting for the token to expire.

### Blobs

- **Existence.** A blob is in `<repo>/<name>` when one of the image's own folders (a tag
  folder, a digest folder or `_uploads/`, each a direct child of `<name>/`) holds
  `sha256__<hex>` with its digest. `HEAD` answers from the
  item's metadata; `GET` streams from the blob store, honours `Range`, and goes through
  the download redirect policy of decision [0029](0029-cdn-delivery-and-download-redirect.md),
  which Artifactory applies to Docker too (research 19). `HEAD` never redirects. Both send
  `Docker-Content-Digest`, `Content-Length` and `Accept-Ranges: bytes`.
- **Uploads.** `POST .../blobs/uploads/` opens a session in `pkg/blob/session` recorded in
  the `uploads` store, and answers 202 with an absolute `Location` from the base-URL
  resolver, `Range: 0-0` and `Docker-Upload-UUID`. `OCI-Chunk-Min-Length` is sent only when
  the blob backend needs a minimum part size. With `?digest=` and a body the upload is
  monolithic and answers 201. `PATCH` appends: with `Content-Range` the chunk must start at
  the current offset or the answer is 416 `BLOB_UPLOAD_INVALID`; without it the body is
  streamed. `PUT ?digest=` (with an optional final chunk) verifies the digest, answering
  400 `DIGEST_INVALID` and discarding the session on a mismatch, and otherwise 201 with the
  blob's `Location` and `Docker-Content-Digest`. `GET` on a session answers 204 with `Range`;
  `DELETE` cancels it with 204. A whole layer is never held in memory.
- **Where a finished blob lives.** A finished upload becomes the item
  `<name>/_uploads/sha256__<hex>`, as on Artifactory. A manifest push links the blobs it
  references into its own folder, by reference to the stored content, never by copying
  bytes, so the manifest no longer needs the `_uploads/` item. The garbage-collection task
  removes `_uploads/` items 24 hours after their upload, as it reaps stale sessions
  (decision [0010](0010-retention-trash-and-gc.md)); a later push that reuses the blob
  finds it in a tag or digest folder.
- **Digest algorithm.** Only `sha256` is accepted. `digest-algorithm=sha256` or no
  parameter opens a session; any other algorithm answers 400 `DIGEST_INVALID`.
- **Mounts.** `?mount=<digest>&from=<other name>` names the source as the client addresses
  it (on the repository-path method `<repo>/<image>`). When the caller can read the source
  and the blob is there, the mount answers 201 and links the blob into the target's
  `_uploads/`; otherwise it falls back to 202 with a new session. A `POST` with `mount` and
  no `from` opens a session (202): nothing is mounted automatically, so a client learns
  nothing about blobs in repositories it cannot name.
- **Deleting a blob** answers 405 `UNSUPPORTED`. A blob's life follows the manifests that
  reference it: deleting a tag or manifest removes its folder, and the content is reclaimed
  by garbage collection when no item references it.

### Manifests

- **Accepted types.** OCI image manifest and image index, Docker image manifest v2
  schema 2 and Docker manifest list. A schema-1 manifest
  (`application/vnd.docker.distribution.manifest.v1+json` or `+prettyjws`) answers 400
  `MANIFEST_INVALID` whatever `blockPushingSchema1` says; the setting is stored and read
  back as configured. Any other type answers 400 `MANIFEST_INVALID`, and so does a
  `schemaVersion` other than 2, which keeps schema 1 out whatever `Content-Type` claims.
- **Validation.** The body must parse, and a `mediaType` in it must equal `Content-Type`.
  Every referenced blob (config and layers) must be in the image, and every child of an
  index must be a manifest in the image, or the answer is 400 `MANIFEST_BLOB_UNKNOWN`, one
  error per missing digest. Layers of the non-distributable and foreign media types are not
  required to be present. A `subject` that names no manifest is accepted. Manifests up to
  4 MiB are accepted; a larger one answers 413 with `SIZE_INVALID`. An image manifest's
  labels are read from its config blob.
- **Where a manifest lives.** The stored file holds the exact bytes the client sent.
  - Pushed by tag: `<name>/<tag>/manifest.json` for an image, `<name>/<tag>/list.manifest.json`
    for an index or list, with the image's config and layers linked into the tag folder
    as `sha256__<hex>`.
  - Pushed by digest: `<name>/sha256__<hex>/manifest.json` (or `list.manifest.json`),
    with its blobs linked beside it. Platform manifests of a multi-architecture image live
    here, which is where `jf` finds them.
  - Pushing a different manifest over an existing tag needs Delete, as Artifactory's
    Strict Tag Overwrite does, and replaces the folder's contents in one transaction.
    Pushing the identical manifest again changes nothing and needs only Deploy, so a
    repeated `docker push` by a deployer succeeds and the build properties `jf` set on
    the layers stay.
  - Two tags are reserved and answer 400 `TAG_INVALID`, though the tag grammar allows
    them: `_uploads`, which would collide with the uploads folder, as on Artifactory, and
    any tag starting with `sha256__`, whose folder would read back as a push by digest.
- **Properties** set on every stored manifest, in the same transaction:
  `docker.manifest` (the folder's name, the tag or `sha256__<hex>`),
  `docker.manifest.digest` (`sha256:<hex>`), `docker.manifest.type` (the media type),
  `docker.repoName` (`<name>`) and `sha256` (the hex digest). An image manifest's config
  labels become `docker.label.<label>` properties. A manifest with a `subject` also gets
  `oci.subject` (the subject digest) and `oci.artifactType` (the descriptor's
  `artifactType` as the referrers API reports it).
- **Push response.** 201 with `Location` (`.../manifests/<digest>`), `Docker-Content-Digest`,
  and `OCI-Subject` when the manifest has a `subject`.
- **`end-7b`.** `PUT .../manifests/<digest>?tag=a&tag=b` stores the manifest by digest and
  each tag, and answers 201 with an `OCI-Tag` header for each tag accepted.
- **Reads.** `GET` and `HEAD` by tag read the tag folder's manifest; by digest they read the
  manifest with that SHA-256 in any of the image's own folders. `Content-Type` is
  `docker.manifest.type`, and `Docker-Content-Digest` and `ETag` carry the digest. A
  manifest is never converted: a request whose `Accept` lists manifest types but not the
  stored one answers 404 `MANIFEST_UNKNOWN`, and no `Accept` or a wildcard gets the stored
  type. A reference that is neither a valid
  tag nor a valid digest answers 404 `MANIFEST_UNKNOWN` on reads and deletes, and 400
  `MANIFEST_INVALID` on a push. Manifests never redirect.

### Tags, catalogs and referrers

- **Tags list.** The tags of `<name>` are its tag folders that hold a manifest; digest
  folders are not tags. They are sorted as the specification says, case-insensitively, with
  ties broken by byte order, limited by `n`, continued after `last`, with a `Link` header
  (`<...>; rel="next"`) when more follow. An image with no tags but stored manifests
  answers `{"name","tags":[]}`; an unknown image answers 404 `NAME_UNKNOWN`.
- **Catalogs.** Image names are the folders that hold at least one manifest, sorted the
  same way and paginated the same way, with the images the caller cannot read left out.
  The registry-wide catalog prefixes each with its repository key (decision 0003).
- **Referrers.** `GET .../referrers/<digest>` answers 200 with an OCI image index of the
  manifests in `<name>` whose `oci.subject` is the digest, each descriptor carrying
  `mediaType`, `digest`, `size`, `artifactType` and the manifest's annotations. An unknown
  digest gives an empty index. `artifactType` filtering is supported and announced with
  `OCI-Filters-Applied: artifactType`. No `referrers.json` file is written; the answer
  comes from the properties.

### Deletes

- `DELETE .../manifests/<tag>` removes the tag folder and answers 202.
- `DELETE .../manifests/<digest>` removes every tag folder and the digest folder holding
  that manifest and answers 202; an index's platform manifests stay, as they are manifests
  of their own.
- `DELETE /artifactory/api/docker/<repo>/v2/delete` with `{dockerRepository, tag}` removes a
  multi-architecture tag with the platform manifests no other tag or index in the image
  references, as Artifactory does from 7.118.0, and answers 200.
- Every delete needs Delete, sends the removed items to the trash can when it is on
  (decision 0010), and updates the package index in the same transaction.

### Permissions

| Operation | Needs |
|---|---|
| `HEAD`/`GET` a blob or manifest, tags list, referrers, `parentManifests` | Read on `<name>/` |
| Catalogs | Read; images the caller cannot read are left out |
| Upload a blob, push a manifest by digest or to a new tag | Deploy on `<name>/` |
| Push a different manifest over an existing tag | Deploy and Delete; the identical manifest needs Deploy only |
| Mount | Read on the source and Deploy on the target |
| Delete a tag, a manifest or a multi-architecture tag | Delete |
| Promote | Read on the source (and Delete when moving), Deploy on the target (and Delete when a target tag holds a different manifest, as DV-035 has it for a push) |

The properties the registry sets on manifests need no Annotate. Anonymous callers act as
the anonymous user.

### JFrog REST operations

- **Promote** (`POST /artifactory/api/docker/<repo>/v2/promote`): copies or moves a tag, or
  every tag of the image when `tag` is absent, to `targetRepo` under
  `targetDockerRepository` and `targetTag`, which default to the source's names. Each tag
  folder is copied with its blobs linked, never copied as bytes; a multi-architecture tag
  takes its platform manifests' digest folders with it. Answers 200 with a plain-text
  message, 400 for a malformed body, 404 for an unknown repository, image or tag. Local and
  federated sources and targets of the same package type.
  - The promotion goes through the item service's copy and move, so the trash can, the
    package index and events follow; a move keeps the items' history, and the platform
    manifests nothing left in the source image names go with it.
  - A target tag holding a different manifest is replaced as a push replaces it, kept under
    Tag Retention; one holding the same manifest is left as it is. The target image's Max
    Unique Tags then applies.
  - The landed manifest's `docker.repoName` and `docker.manifest` name the target image and
    tag.
  - `targetTag` without `tag` answers 400: promoting every tag keeps their names. A
    promotion onto itself (same repository, image and tag) answers 400.
- **Parent manifests** (`POST .../v2/parentManifests`): the indexes in the repository that
  list the manifest the tag names, as `{dockerRepository, digest, tags}`. A push stores an
  index only where its platforms already are, so the tag's image is searched; each index
  appears once, ordered by digest, with the tags it sits under (a digest folder adds none).

### Package index

Decision [0018](0018-package-index.md) applies with an image as the package, a tag as the
version, the tag folder's items as the version's files and the push time as the order. Max
Unique Tags and Tag Retention follow Artifactory's documented rules (R23 5.3):
- after each push, the pushed image's oldest tags beyond `maxUniqueTags` are removed,
  `latest` included, with their retained versions;
- with `dockerTagRetention` at 2 or more, an overwritten tag's previous manifest stays in
  its digest folder, `<name>/sha256__<hex>/`, and the oldest such versions beyond the
  setting minus one are removed;
- a list manifest is removed with the platform manifests no other tag references.

## Rationale

Matching from the end of the path is the only rule that accepts every name the grammar
allows. Sending each code with the reference registry's status keeps the clients' error
handling on the paths it was written for.

The layout is the one the JFrog CLI reads: tag folders, `sha256__` files, digest folders for
platform images and the five manifest properties. Linking blobs by reference keeps the
layout without storing a layer once per tag.

Three choices depart from Artifactory and are in the
[deviations register](../../implementation/deviations.md): schema 1 is refused (DV-034),
Strict Tag Overwrite has no switch (DV-035), and referrers come from properties rather than
a `referrers.json` (DV-036).

**Alternatives considered:**
- *Accepting schema 1, honouring `blockPushingSchema1`*: Docker has removed schema 1 from
  its engine, so no supported client pushes or pulls it, and accepting it means verifying
  signed JWS manifests for nothing.
- *`GET /v2/` answering 200 to anonymous callers when anonymous access is on*: one of
  JFrog's walk-throughs reads that way, and the same article says such a client then never
  sends its credentials. A challenge on every unauthenticated probe keeps `docker login`
  checking the password and pushes authenticating; anonymous pulls still work through the
  anonymous token.
- *Blob `DELETE` with 202*: a blob a manifest still references would then either stay
  readable after a successful delete, which the conformance suite rejects, or break the
  manifest. Artifactory's own deletes are by tag and image. 405 is accepted by the suite
  and by every teardown in it.
- *Automatic mounting without `from`*: would answer 201 for a blob found anywhere the
  caller can read, telling clients about content outside the repository they named.
- *A `referrers.json` file, as Artifactory keeps*: a read-modify-write file under
  concurrent pushes, in a format no source shows, that no client reads. Properties answer
  the same question in the transaction that stores the manifest.
- *A separate table for finished uploads*: `_uploads/` items reuse the item tree and match
  what Artifactory shows; `jf` already excludes the folder from its searches.

## Constraints

- A manifest larger than 4 MiB is refused.
- A tag folder of `team` and the image folder of `team/app` are the same path when both
  exist, as on Artifactory: a folder is a tag because it holds a manifest file, and an
  image because a child of it is a tag.
- Under the subdomain method `from` in a mount carries no repository key, so mounts cross
  images of one repository only.
- What Artifactory answers for blob and manifest deletes, anonymous probes and schema-1
  pushes, the label prefix, Tag Retention's folder name and the `referrers.json` format are
  inferred (R23 section 10) and on the live-capture list (#216).

## Verification

- Unit tests per operation for each error code and status in the table, the headers named
  above, chunk ordering, resumed sessions after a restart, digest mismatches, mounts with
  and without read permission, and a streamed upload that never buffers a layer.
- Layout tests: after `docker push`, a multi-architecture `buildx` push and a push by digest,
  AQL finds the files and properties `jf` searches for (R23 6).
- The OCI conformance job (decision [0015](0015-testing-and-acceptance.md)) with all four
  categories.
- Acceptance: `docker`, `crane`, `oras` and `cosign` round trips, `jf docker push` and
  `jf docker pull` with `--build-name` and `--build-number` followed by
  `jf rt build-publish`, `jf rt build-docker-create` for an image pushed by `crane`, and
  `jf rt docker-promote`. `jf docker build` is not covered, as after `docker buildx` it runs
  the same search as a push, and neither is `jf docker scan`, which calls Xray's stub.

## References

- [Research 23](../23-docker-oci-registry.md)
- OCI Distribution Specification v1.1.1: <https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md>
- `distribution/distribution` v3.0.0 (`678b095af9c9`), `registry/api/errcode/register.go`
- jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/ocicontainer`
- <https://docs.jfrog.com/artifactory/docs/additional-docker-information.md>
- <https://docs.jfrog.com/integrations/reference/promotedockerimage.md>
