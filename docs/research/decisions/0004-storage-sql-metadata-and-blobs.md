# 0004: Storage: SQL metadata, item model and content-addressed blobs

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))

## Context

The server must run as one static binary with no external dependencies and must also run
against shared services (PostgreSQL, S3-compatible object storage) when operators want
durability beyond one disk. hostweave's decision 0006 established `database/sql` behind a
dialect layer with one contract suite on every backend.

Artifactory's storage API exposes items with a `path` that has a leading slash, AQL
exposes the same items with a parent `path` of `.` for the root and a separate `name`,
checksum deploy looks up existing binaries by sha1 or sha256, Maven clients PUT `.sha1`
and `.md5` files beside artifacts, the JFrog CLI downloads with `Range` requests, and
`downloadRedirect` means a 302 to a signed object-store URL or, when the store has one, a CDN
([0029](0029-cdn-delivery-and-download-redirect.md)). Large uploads arrive as Docker
chunks and as the CLI's multipart protocol.

## Decision

**Metadata.** Store contracts in `pkg/store` with narrow interfaces per concern (repos,
items, properties, stats, package index, users, groups, permissions, tokens, policies,
webhooks, projects, builds) composed into `Store` with `Tx`. `internal/sqlstore` follows
hostweave's `sqlcommon` pattern with two dialects: SQLite (`modernc.org/sqlite`, CGO-free,
WAL, one writer connection; the default) and PostgreSQL (`pgx/v5/stdlib`). Embedded
migrations per dialect. An in-memory store exists for unit tests.

**Item canonical form.** The items table stores `repo_key`, `path` (the parent directory,
`.` for the root), `name`, `depth`, `type`, `cached`, `size`, `mime_type`, timestamps and
actors, `actual_md5`, `actual_sha1`, `actual_sha256`, `original_md5`, `original_sha1`. The
item table also keeps `sha512` and `original_sha512` (below), which AQL does not name.
The storage API layer prefixes `/` on `path` and joins `path` and `name`; AQL reads the
columns as they are. Properties are an entity-attribute-value table keyed by item.

**Property keys and values.** JFrog's documentation limits property keys to 255 characters
and values to 2400 ("JFrog Properties"). A value is kept to 2400 UTF-16 code units, and a
build or module property value to 2048, the limit of Artifactory 6.20's `BuildsDao` and
`BuildModulesDao`, which neither the documentation nor the 7.x release notes cover. As 6.20
does (`PropertiesDao`), the stores cut a longer value on write rather than refusing it, but
never inside a surrogate pair; whether 7.x still cuts is unconfirmed (#216). A key longer than 255
characters is refused with 400 on every route that sets properties, and the stores refuse
it too ([DV-059](../../implementation/deviations.md#api-behaviour), #340). On PostgreSQL no
index holds a whole value, because a B-tree entry is at most 2704 bytes: the property
tables are unique on `md5(value)`, and value lookups use an index on the first 255
characters, which AQL narrows on before it compares whole values (migration 0013, #338).
The property limits were checked against Artifactory 7.161.32 (2026-10-17).

**Blobs.** `pkg/blob` is a content-addressed store keyed by sha256. Every upload computes
four checksums in one pass: SHA-256, SHA-512, SHA-1 and MD5. The other three are recorded
in the blob and item index, so checksum deploy, checksum search and checksum files work
from the database. SHA-1 and MD5 are looked up by checksum deploy and the checksum
searches, so they are indexed. SHA-512 is never a lookup key, because whatever holds one
also holds the SHA-256, so it is stored but not indexed.
Backends: `fs` (temp-then-rename into a two-level hex tree), `s3` (aws-sdk-go-v2,
multipart upload for large objects, no rename), and `gcs` and `azure` as decided in
[0027](0027-cloud-blob-backends.md) (compose of temporary parts; staged blocks). The
contract:

- `BeginUpload(ctx) → UploadID`, `Append(ctx, id, chunk)`, `Commit(ctx, id, digest)`,
  `Abort(ctx, id)`; the fs backend keeps a temp directory per upload id, the s3 backend
  persists the multipart upload id in the store so a Docker chunked upload or a CLI
  multipart upload can resume across requests.
- `Open(ctx, digest) → io.ReadSeekCloser` so handlers serve `206` with
  `http.ServeContent`; the s3 backend passes `Range` through.
- `Signs(via)` and `SignURL(ctx, digest, opts)` for redirected downloads, to the object
  store or to a CDN in front of it, with the filename and media type as response
  overrides. `Signs` is fixed at construction, so the server knows without trying whether a
  store can sign ([0029](0029-cdn-delivery-and-download-redirect.md)).
- `Delete`, `Exists`, `Stat`.

**Checksum sidecars.** `.sha1`, `.md5`, `.sha256` and `.sha512` files are never stored.
Artifactory's file info names only `sha1`, `md5` and `sha256`, so SHA-512 reaches clients
as a `.sha512` file and nowhere else in the REST API.
GET and HEAD serve them from item metadata. PUT validates the client value against the
artifact per the repository's `checksumPolicyType`: `client-checksums` rejects a mismatch
and records the client value as `original_*`; `server-generated-checksums` records and
ignores. Mismatches feed the bad-checksum search.
Serving and checking `.sha512` files is an artifactweave extension, a deviation registered
by #378. No 7.x page or release note documents SHA-512 for generic or Maven files, and an
open request asks JFrog to add it for generic files
(**[J]** [RTFACT-30948](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30948)). How
7.x treats an uploaded `.sha512` file is on
[#216](https://github.com/weaveplatform/artifactweave/issues/216).

**Garbage collection.** Items reference blobs; deleting an item moves it to the trash can
and the blob stays referenced until the trash entry is purged. GC runs as a task behind
`POST /api/system/storage/gc` and removes unreferenced blobs, feeding the `storageinfo`
numbers.

**Single process.** SQLite means one server process. PostgreSQL is supported as a
database but artifactweave v1 is still one process: singleflight, upload sessions and the
scheduler are in-process. High availability (leader leases, shared blob stores only,
store-backed upload sessions) is a later decision.

## Rationale

Matching hostweave keeps the pattern, the contract suite habits and the reviewers shared.
sha256 content addressing with sha512, sha1 and md5 recorded gives every client what it
asks for: the JFrog CLI sends sha1, sha256 and md5 on upload, Maven wants sha1 and md5,
Docker wants sha256, npm's `dist.integrity` is a SHA-512, Gradle uploads `.sha512` files
and Maven Resolver 2 can verify them, and NuGet (P8) uses `.nupkg.sha512`.
An explicit upload-session contract is the only way Docker chunked uploads, S3 multipart
and the CLI's multipart protocol can share one implementation.
The CLI's multipart protocol (#70) stages out-of-order parts locally and appends them to
one such session; see [research 09](../09-jfrog-cli-compatibility.md#multipart-upload).

**Alternatives considered:**
- *sha1-keyed filestore like Artifactory*: sha1 is deprecated for integrity and sha256 is
  what Docker and the CLI check; indexing all three costs one table.
- *SHA-3 as well*: no package client and no Artifactory API asks for it, so it would
  cost CPU on every upload for a digest nothing reads (maintainer decision, 2026-10-16).
  The checksum set is defined in one place (`pkg/blob.Hasher` and `types.Checksums`), so
  adding an algorithm later is a small change if an ecosystem needs one.
- *Storing sidecar files as items*: simpler, but then a sidecar can disagree with its
  artifact and `badChecksum` has nothing to compare.
- *MySQL as a third dialect*: deferred; nothing in scope needs it.
- *Keeping 4000 characters of a value*: Artifactory 6.20 keeps 4000 on most databases and
  2400 on PostgreSQL (`db.postgres.property.value.max.size`), but 7.x documents 2400, so
  every store keeps 2400 and a value reads back the same whichever database holds it.
- *bun, ent, gorm*: heavier abstractions and harder to test to 95%.

## Artifactory 7.x check

The decision stands against 7.161.32. The audit of P1 to P7
([#378](https://github.com/weaveplatform/artifactweave/issues/378)) found these differences
in the deploy and checksum paths, which
[research 02](../02-artifactory-core-api-inventory.md#artifactory-7x-check) lists with the
rest of the storage API:
- 7.x differs: a checksum deploy reuses content only when the caller can read an existing artifact with that checksum, and answers 404 otherwise ([deployartifactbychecksum](https://docs.jfrog.com/artifactory/reference/deployartifactbychecksum.md)); fixed by [#379](https://github.com/weaveplatform/artifactweave/issues/379).
- 7.x differs: a re-deploy whose declared checksum equals the stored file's needs no delete permission, unless `enforce.permission.check.on.identical.checksum.deploy=true` ([known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues), 7.26.3, RTDEV-24969); fixed by [#400](https://github.com/weaveplatform/artifactweave/issues/400).
- 7.x differs: under `client-checksums`, a `.sha1` or `.md5` file serves the client's checksum, and answers 404 while the client has sent none (**[KB]** [Understanding Artifactory Checksum Policies](https://jfrog.com/help/r/what-are-client-checksum-server-checksum-and-checksum-policy-in-local-repositories/what-are-client-checksum-server-checksum-and-checksum-policy-in-local-repositories)); fixed by [#399](https://github.com/weaveplatform/artifactweave/issues/399).
- 7.x differs: every deploy records the SHA-256 as an original checksum, including when the client sent none (**[KB]** the same article). Since [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed) (RTDEV-80481), a checksum deploy that sends only a SHA-256 no longer reports "Client did not publish a checksum value"; fixed by [#399](https://github.com/weaveplatform/artifactweave/issues/399).

## Constraints

- Every query is written for two dialects; the contract suite is the guard.
- SQLite supports one server process. Two processes against one SQLite file is a
  configuration error the binary refuses.
- S3 multipart requires parts of at least 5 MiB; the s3 backend buffers to that size.
- The items table is the hot path; indexes on `(repo_key, path, name)`, `actual_sha1`,
  `actual_sha256` and the EAV property table are part of the migrations.

## Verification

- `pkg/store/storetest` and `pkg/blob/blobtest` contract suites run against the in-memory
  store, SQLite, PostgreSQL (testcontainers) and MinIO (testcontainers) on Linux CI and
  skip cleanly where Docker is absent.
- Upload-session tests resume across simulated process boundaries on both blob backends.
- Ranged download and checksum-sidecar tests in the item handler package.
- Migration up, down, up tests per dialect.

## References

- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md), Artifacts
  and Storage section
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), upload and download rows
- `weaveplatform/hostweave@main`, `docs/research/decisions/0006-sql-storage-backends.md`
- <https://pkg.go.dev/modernc.org/sqlite>
- <https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib>
- <https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3>
