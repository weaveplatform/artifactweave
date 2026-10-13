# 0027: Cloud blob backends: Google Cloud Storage and Azure Blob Storage

Status: Proposed

## Context

Decision [0004](0004-storage-sql-metadata-and-blobs.md) defines the content-addressed blob
contract (`pkg/blob`) with `fs` and `s3` backends. Artifactory also stores binaries on
Google Cloud Storage and Azure Blob Storage, and teams leaving it often run on those
clouds.

The contract asks four things of a backend:
- An upload is a session that resumes in a later HTTP request or another process, from
  state the metadata store persists.
- Chunks arrive in any size (Docker PATCH, the JFrog CLI's multipart upload).
- Blobs reach many GiB.
- The content-addressed name (`sha256/<ab>/<hex>`) is known only at commit, unless the
  client declared the digest up front.

[Research 18](../18-cloud-blob-storage.md) records what each service and its official Go
SDK offer.

## Decision

**Shared session layer.**
- The object-store backends (s3, gcs, azure) persist their sessions through one shared
  layer, `pkg/blob/session`. It holds:
  - the pending part buffer;
  - the saved `blob.Hasher` state;
  - the size and the declared digest;
  - a backend-specific JSON state document.
- Sessions are stored in the metadata store's `uploads` table under a per-instance
  backend name (`<backend>:<store name>`), so several stores of one kind (a mirror, or a
  hot and a cold tier) never share or misattribute sessions.

**`gcs`: compose of temporary part objects.**
- **Layout:** blobs at `<prefix>sha256/<ab>/<hex>`; parts and folds under
  `<prefix>_uploads/<id>/`, named with an increasing sequence and never reused.
- **Append:**
  - Buffers to `part_size`: 16 MiB by default, a multiple of 256 KiB, at least 1 MiB.
  - Writes each full part as one object with `DoesNotExist`, as a single retryable
    request.
  - A 412 on a part counts as success when the existing object's size and CRC32C match.
  - When a session would exceed 32 sources, it composes them into a fresh fold object
    with `DeleteSourceObjects`.
  - The session state lists the parts and the fold with their generations, sizes and
    CRC32C, plus the running CRC32C of all appended bytes.
- **Commit:**
  - Verifies the declared digests.
  - A session with no parts writes its pending bytes straight to the final key.
  - Otherwise it flushes the tail as a last part and composes everything onto the final
    key with `DoesNotExist` and `DeleteSourceObjects`.
  - A 412 means the blob is already stored, which the contract treats as success.
  - It then checks the size and CRC32C of the result, deletes what is left under
    `_uploads/<id>/`, and forgets the session.
  - A declared digest changes nothing here: compose lands on the final key either way,
    so there is never a copy.
- **Abort:** deletes the session's objects, tolerating not-found.
- **Reads:**
  - A lazy reader over `NewRangeReader`, reopened on `Seek`. Writes never set a content
    encoding, so GCS never transcodes a blob; `ReadCompressed(true)` is not used because
    the SDK refuses it with JSON reads.
  - Stat, Exists and Delete use object attributes; Delete treats not-found as success.
  - Walk lists `sha256/` only, with the attribute selection limited to name and size.
- **Presigning:** V4 signed GET URLs. Local signing when a key is configured, otherwise
  IAM `signBlob`.
- **Client:**
  - The HTTP client with JSON reads.
  - Credentials from Application Default Credentials or a service-account file.
  - An optional endpoint for emulators.
  - The build sets `disable_grpc_modules`.

**`azure`: staged blocks with a server-side copy only when the digest was not declared.**
- **Layout:** the same blob layout. Temporary blobs live at `<prefix>_uploads/<id>`.
- **Append:**
  - Buffers to `part_size` (8 MiB by default).
  - Stages each full part as a block with the ID `base64(<upload id>-<six-digit
    sequence>)`. That is standard base64, the same length for every block, and
    re-staging the same ID is idempotent.
  - Refuses to grow past 50,000 blocks.
- **Commit with a declared digest:**
  - The blocks were staged on the final name.
  - Commit runs `CommitBlockList` there with `If-None-Match: *`.
- **Commit without a declared digest:**
  1. The blocks were staged on the temporary blob; commit that first.
  2. Copy it into the final name with parallel `StageBlockFromURL` calls, each covering
     `copy_range_size` (100 MiB by default), with bounded concurrency and
     `SourceIfMatch` on the temporary blob's ETag.
  3. The source is authorised with a service SAS when the store has the account key, or
     with a bearer token under Entra ID.
  4. Commit the final block list with `If-None-Match: *` and delete the temporary blob.
- **Small blobs:** a session that never filled a part is written with one Put Blob and
  `If-None-Match: *`.
- **Conflicts:** `BlobAlreadyExists` or `ConditionNotMet` at commit means the blob is
  stored, once a stat of the final name confirms it.
- **Reads:**
  - A ranged reader with the SDK's retry reader and ETag pinning, short-circuiting at the
    end of the blob.
  - Delete includes snapshots and treats `BlobNotFound` as success.
  - `ContainerNotFound` is reported as a configuration error, never as a missing blob.
- **Presigning:**
  - A service SAS signed with explicit container and blob names.
  - Or a user delegation SAS from a cached key under Entra ID.
  - The start time is backdated 15 minutes.
- **Credentials:** shared key, connection string, SAS token, `DefaultAzureCredential`,
  managed identity or workload identity.

**Both backends.** Following the s3 pattern, each backend wraps its SDK in a narrow `API`
interface, so the backend can be tested on every platform against a fake that models the
real service (including the behaviours the emulators get wrong) and has fault injection.

## Rationale

The design follows from what each SDK can actually do (research 18).

**GCS.**
- The Go SDK cannot resume a resumable-upload session from persisted state and has no XML
  multipart support.
- Compose is fully supported, accepts parts of any size and has no limit on total
  components. It can write directly to the content-addressed name with a "must not exist"
  precondition, which makes the commit idempotent and removes the copy that the s3 backend
  needs above 5 GiB.

**Azure.**
- Staged blocks are server-side state that any process can commit, which is exactly the
  resumable session the contract needs.
- The only cost is a server-side copy when the digest was not declared. Put Block From URL
  keeps the final blob invisible until an atomic commit; the asynchronous Copy Blob
  exposes a zero-length placeholder while pending.

**Testing with fakes.** The emulators (fake-gcs-server, Azurite) skip checks that matter:
compose preconditions, `deleteSourceObjects`, copy-source authorisation and block limits.
So a fake that models the real services carries the semantics, and the emulators check
the SDK wiring on Linux CI.

**Alternatives considered:**
- *GCS resumable uploads over raw HTTP:* this works, but means handling the session URI
  (a bearer credential that would need sealing), the 256 KiB chunk rule and a final copy.
  fake-gcs-server mishandles resume queries, so it could not be tested faithfully.
- *GCS through the s3 backend with HMAC keys (S3 interoperability):* it rules out Workload
  Identity, `UploadPartCopy` is not documented for GCS, and the AWS SDK's default checksum
  headers break S3-compatible stores.
- *Azure asynchronous Copy Blob:* rejected for its placeholder.
- *Azure pointer blobs* (a small blob naming where the data lives): double the round trips
  on every read and need their own garbage collection.
- *Hierarchical-namespace rename:* atomic, but only on ADLS Gen2 accounts and through a
  separate SDK and endpoint. It remains a possible later fast path.

## Constraints

**GCS.**
- **Cost:** one Class A operation per part and per fold.
- **Soft delete:** on by default, so it bills parts removed by an abort.
- **Storage class:** a bucket whose default class is not Standard bills early deletion
  on temporary parts.
- **Cleanup:** operators should add a lifecycle `Delete` rule on `_uploads/`.

**Azure.**
- **SDK API version:** the SDK sends API version 2026-12-06 and cannot be pinned lower.
  The emulator needs `--skipApiVersionCheck`.
- **Unverified on real Azure:** two contradictions in Microsoft's documentation (the Put
  Block From URL range limit, and Shared Key authorisation of a same-account Copy Blob
  From URL source) are settled only by a real-cloud run.
- **Accounts without Shared Key:** an account that disallows Shared Key needs Entra ID for
  both the copy source and presigning.
- **Dependency weight:** the azblob `container` package pulls in `apache/arrow-go`.

## Verification

**Both backends:**
- `blobtest.Run` against the backend's fake on every platform.
- `blobtest.Run` against the emulator on Linux CI, pinned by digest and skipped where
  Docker cannot run Linux containers:
  - fake-gcs-server 1.56.1;
  - Azurite 3.37.0.
- Signing tests with generated keys and no network.
- The session-layer test shows that two stores of one kind keep separate sessions in a
  single `uploads` table.

**Real cloud:** an opt-in workflow runs the contract against real accounts.

## References

- [Research 18: cloud blob storage](../18-cloud-blob-storage.md), with the SDK paths and
  documentation for every fact above.
- `cloud.google.com/go/storage@v1.69.0`: `copy.go` (Composer), `writer.go`, `bucket.go`
  (SignedURL).
- `github.com/Azure/azure-sdk-for-go/sdk/storage/azblob@v1.8.2`: `blockblob/client.go`,
  `sas/service.go`.
- JFrog binary providers `google-storage-v2` and `azure-blob-storage-v2` on
  docs.jfrog.com.
