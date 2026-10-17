# 0004: Storage: SQL metadata, item model and content-addressed blobs

Status: Proposed

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

**Property values.** A property value is kept to 4000 UTF-16 code units, and a build or
module property value to 2048, the limits of Artifactory 6.20 (`PROP_VALUE_MAX_SIZE` in
`PropertiesDao`, `BuildsDao` and `BuildModulesDao`). Like Artifactory, the stores cut a
longer value on write rather than refusing it, but never inside a surrogate pair. On
PostgreSQL no index holds a whole value, because a B-tree entry is at most 2704 bytes: the
property tables are unique on `md5(value)`, and value lookups use an index on the first 255
characters, which AQL narrows on before it compares whole values (migration 0013, #338).

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
- *Artifactory's PostgreSQL limit on property values*: Artifactory cuts them to 2400
  characters there (`db.postgres.property.value.max.size`), because its indexes hold
  whole values. With only a prefix indexed, every store keeps 4000, so a value reads back
  the same whichever database holds it ([DV-059](../../implementation/deviations.md#data-model)).
- *bun, ent, gorm*: heavier abstractions and harder to test to 95%.

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
