# Cloud blob storage: Google Cloud Storage and Azure Blob Storage

Checked against: Artifactory 7.161.32

Research baseline: **2026-10-13**.

Artifactory stores binaries on Google Cloud Storage and Azure Blob Storage as well as S3
(the `google-storage-v2` and `azure-blob-storage-v2` binary providers). This report records
what the two services and their official Go SDKs offer for the blob contract in
`pkg/blob`, where it matters:

- Uploads are sessions resumed across HTTP requests and processes from persisted state.
- Chunks arrive in arbitrary sizes (Docker PATCH, JFrog CLI multipart).
- Blobs reach many GiB.
- The content-addressed name is known only at commit.

Decision [0027](decisions/0027-cloud-blob-backends.md) records the backend design and
decision [0028](decisions/0028-blob-mirroring.md) the mirroring that section 3 informs.

Source abbreviations:

| Short form | Means |
|---|---|
| **GCS-SDK** | `cloud.google.com/go/storage@v1.69.0` (module source in the Go module cache) |
| **API** | `google.golang.org/api@v0.288.0` |
| **FGS** | `github.com/fsouza/fake-gcs-server@v1.56.1` |
| **AZB** | `github.com/Azure/azure-sdk-for-go/sdk/storage/azblob@v1.8.2` |
| **AZC** | `.../sdk/azcore@v1.23.3` |
| **AZI** | `.../sdk/azidentity@v1.14.1` |
| **AZT** | Azurite v3.37.0 |
| **GCS docs** | https://docs.cloud.google.com/storage/docs/ |
| **MS:x** | https://learn.microsoft.com/en-us/rest/api/storageservices/x |

**[probe]** marks a behaviour observed by running the SDK against the emulator on
2026-10-13. **[cloud]** marks one observed against a real account by the
`cloud-conformance` probes (section 4), with the dates of the runs.

## 1. Google Cloud Storage

### 1.1 SDK

- **Release:** v1.69.0, 2026-10-01. Releases come roughly every two weeks (GCS-SDK/CHANGES.md:4-140).
- **Go:** minimum 1.26.0 (GCS-SDK/go.mod:3).
- **Dependencies:** `cloud.google.com/go/auth`, `google.golang.org/api`, gRPC, OpenTelemetry and the GCP metric exporter (GCS-SDK/go.mod:7-31).
- **Binary size:** a trivial client links 463 packages into a 47 MB binary. With the `disable_grpc_modules` build tag, which drops the rls, xds and directpath imports, it links 325 packages into 29 MB (GCS-SDK/grpc_dp.go:1,20-21; doc.go:398-401).
- **Which client:** use the HTTP client, `storage.NewClient` with `storage.WithJSONReads()`. Reads default to the XML API (GCS-SDK/reader.go:93-97; option.go:128-142). The gRPC client's Direct Connectivity only helps inside GCP with a co-located bucket (doc.go:362-396).

### 1.2 Multi-request uploads with persisted state

| Mechanism | Service limits | Go SDK support |
|---|---|---|
| JSON resumable upload | session lives up to a week; the session URI is a bearer credential; chunks must be multiples of 256 KiB except the last ([resumable-uploads], [performing-resumable-uploads]) | **cannot resume a session from persisted state**: `storage.Writer` exposes no session URI (GCS-SDK/writer.go:45-252); the generated client consumes `Location` internally (API/storage/v1/storage-gen.go:11196) and the resumable code is in an internal package (API/internal/gensupport/resumable.go:30-33) |
| Appendable objects (`NewWriterFromAppendableObject`) | gRPC only, zonal Rapid buckets only, which allow no JSON API writes, no resumable uploads, no soft delete ([rapid-bucket]) | takeover by generation (GCS-SDK/storage.go:1293-1317), not usable on general buckets |
| XML API multipart upload | parts ≥ 5 MiB except the last, ≤ 5 GiB, ≤ 10,000 parts, objects ≤ 5 TiB; no preconditions; no MD5 on the result; unfinished uploads persist until a lifecycle rule aborts them ([quotas], [multipart-uploads], [request-preconditions]) | **none**: the only reference is the lifecycle constant `AbortIncompleteMPUAction` (GCS-SDK/bucket.go:664) |
| Compose | 1 to 32 sources per request; **no limit on total components** (the old 1,024 cap is gone: "`componentCount` saturates at 2,147,483,647", [quotas]); same bucket and storage class; any destination name; destination `ifGenerationMatch` (0 = must not exist) and per-source generation preconditions; `deleteSourceObjects` hard-deletes the sources ([compose reference], [composite-objects]) | full: `dst.ComposerFrom(srcs...)` and `Composer.Run` (GCS-SDK/copy.go:155,184), `DeleteSourceObjects` (copy.go:162-182), retried only with a destination precondition (copy.go:233) |

The SDK's own experimental parallel upload uses the compose pattern. It writes parts with
`ChunkSize=0` and composes them in a 32-way tree with `DeleteSourceObjects` under a
temporary prefix (GCS-SDK/pcu.go:271-290,509-575,36-37).

Composite objects carry a CRC32C but **no MD5**, and their ETag is not an MD5
([composite-objects]). The docs say compose "ignores any CRC32C value you provide"; the
SDK says the compose is "rejected" on a mismatch (GCS-SDK/copy.go:168-173). **[cloud]** The
docs are right: compose accepts a wrong CRC32C (2026-10-13 and 2026-10-16, section 4), so
the CRC32C must be compared client-side, as the SDK's parallel upload does
(pcu.go:579-582). **[cloud]** A stale source generation answers 404 `notFound`, not 412,
and a destination that must not exist answers 412 `conditionNotMet`. Nothing in the SDK's
read path relies on MD5; the HTTP reader validates CRC32C on full reads only
(GCS-SDK/http_client.go:1484-1515,1675-1688).

**Cost.** One Class A operation per part and per compose; deletes are free ([pricing]). A
10 GiB blob in 16 MiB parts costs about 640 part writes, 21 folds and one final compose,
about $0.003 at $0.005 per 1,000.

### 1.3 Reads, writes and retries

- **Ranged reads:** `NewRangeReader(ctx, offset, length)`. A negative length reads to the end (GCS-SDK/reader.go:102-130).
- **Transcoding:** an object stored with `Content-Encoding: gzip` is served whole whatever range is requested. `ReadCompressed(true)` prevents decompression (reader.go:108-112; storage.go:1172-1177), but with JSON reads the SDK refuses it client-side ("custom Accept-Encoding headers not allowed"; found by the emulator run of the gcs backend), so the safeguard is that blobs never carry a content encoding.
- **Reopening:** the reader pins the first response's generation as a precondition when it reopens (http_client.go:1618-1628).
- **Sentinel errors:** `ErrObjectNotExist` and `ErrBucketNotExist` (storage.go:67-73). Other errors are `*googleapi.Error` (doc.go:275-312).
- **Small uploads:** `Writer.ChunkSize` defaults to 16 MiB. Content smaller than `ChunkSize` goes as one buffered, retryable request. Content of exactly `ChunkSize` or more becomes a resumable upload, because end of input is only seen on the next read (API/internal/gensupport/media.go:121-133,194-199; buffer.go:53-68). So a part is written with `ChunkSize` rounded up to a multiple of 256 KiB above its length. `ChunkSize = 0` streams with no retry (writer.go:106-111).
- **Preconditions:** `If(storage.Conditions{DoesNotExist: true})` sends `ifGenerationMatch=0`, which makes the write idempotent and so retried (storage.go:2189-2285; writer.go:477). A conflict is 412. A retried write whose first attempt succeeded also returns 412, so compare `Attrs` (size, CRC32C) before treating 412 as a different object.
- **Retry policy:**
  - The default retries idempotent operations on 408, 429, 5xx and connection resets until the context ends (invoke.go:192-248; storage.go:2570-2587).
  - `Delete` is retried only with a generation precondition (storage.go:1166-1169).
  - Compose and copy are retried only with a destination precondition (copy.go:123,233).
- **Listing and deleting:**
  - `Objects` with `Query{Prefix}` and `SetAttrSelection([]string{"Name","Size"})` (bucket.go:2436; storage.go:1930-2087).
  - Listing is strongly consistent ([consistency]).
  - Deleting a missing object returns a wrapped `ErrObjectNotExist` (http_client.go:512-523).

### 1.4 Signed URLs and authentication

- **`BucketHandle.SignedURL`** signs locally with `GoogleAccessID` plus `PrivateKey` or `SignBytes`. Otherwise it finds the service account from the client credentials or the metadata server and signs through IAM Credentials `signBlob`, which needs `iam.serviceAccounts.signBlob` (bucket.go:187-360; doc.go:252-273). That IAM call uses `context.Background()` (bucket.go:331).
- **Signing scheme:** the default is V2; set `Scheme: storage.SigningSchemeV4`. V4 expiry is at most 7 days (storage.go:556-558,756-760).
- **Testing:** pass a test key through `GoogleAccessID` and `PrivateKey` to sign with no network.
- **Credentials:** Application Default Credentials covers Workload Identity through the metadata server.
  - `option.WithCredentialsFile` and `WithCredentialsJSON` are deprecated. Use `option.WithAuthCredentialsFile(option.ServiceAccount, path)` (API/option/option.go:83-120).
  - `option.WithEndpoint` needs the `/storage/v1/` suffix with the trailing slash.
  - `STORAGE_EMULATOR_HOST` forces unauthenticated HTTP (storage.go:179-205).

### 1.5 Emulators

- **There is no official emulator** (GCS-SDK/doc.go:70).
- **fake-gcs-server v1.56.1:**
  - Runs in process or as a container: image `fsouza/fake-gcs-server:1.56.1@sha256:797ce226d62f947c009dc40246b30cfb456b8473d8241407f9d6f2c04e4d69ef` through testcontainers `modules/fakegcsserver` v0.44.0 (no default image; `StorageURL()` lacks the trailing slash).
  - Supports media, multipart and resumable inserts with `ifGenerationMatch`, compose with the 32-source limit, range reads, listing and rewrite.
- **What fake-gcs-server gets wrong:**
  - Compose **ignores destination and source preconditions and `deleteSourceObjects`**.
  - A missing compose source returns 500.
  - Composite objects get an MD5.
  - Resumable status queries finalise the object.
  - Signed URLs are not validated.
  - (FGS/fakestorage/object.go:1442-1507; FGS/fakestorage/upload.go:770-796)
- **Consequence:** the backend's semantics need a fake that models real GCS. The container then checks the wiring.

### 1.6 JFrog's GCS provider

`google-storage-v2` ([google-storage-v2]) takes these parameters:

| Parameter | Default |
|---|---|
| `bucketName` | |
| `path` | `filestore` |
| `rootFoldersNameLength` | 2 |
| `endpoint` | |
| `bucketExists` | false |
| `testConnection` | true |
| `useInstanceCredentials` | false; true means ADC or GKE, otherwise `gcp.credentials.json` |
| `enableSignedUrlRedirect` | false |
| `signedUrlExpirySeconds` | 30 |
| `maxConnections` | 100 |
| `maxRetries` | 1; retries a 50x from GCS, from 7.96.3 |
| `retryIntervalMillis` | 1000; the wait between those retries |
| proxy settings | |

No multipart or part-size settings are documented. The layout is keyed by SHA-1.

### 1.7 Traps

- **Name limits:**
  - Writing the same object name more than once per second can return 429 ([quotas]), so part and fold names are never reused.
  - Object names are 1 to 1,024 bytes, with no CR or LF.
- **Soft delete:** on by default, with 7-day retention, and soft-deleted objects are billed ([soft-delete]). Parts removed by `DeleteSourceObjects` are hard-deleted; parts removed by an abort are soft-deleted.
- **Storage class:** parts take the bucket's default class. A Nearline or Coldline default bills early deletion on temporary parts ([pricing]).
- **Cleanup:** crashed uploads leave parts behind. A lifecycle `Delete` rule on the `_uploads/` prefix is the backstop ([lifecycle]).

## 2. Azure Blob Storage

### 2.1 SDK

- **Releases:**
  - azblob v1.8.2, 2026-10-01.
  - azcore v1.23.3, 2026-10-06.
  - azidentity v1.14.1, 2026-08-27.
  - All require Go 1.25 (AZB/go.mod:3; AZC/go.mod:3; AZI/go.mod:3).
- **Service API version:** the SDK sends `2026-12-06`, hard-coded per request (AZB/internal/generated/zz_constants.go:8).
- **No override:** setting `ClientOptions.APIVersion` makes every request fail (AZC/runtime/policy_api_version.go:62-64).
- **Weight:** the `container` package imports `apache/arrow-go/v18` for Arrow-format listings (AZB/container/client.go:256-300). azidentity brings MSAL.

### 2.2 Block staging

- **API:** `blockblob.Client` `StageBlock`, `StageBlockFromURL`, `CommitBlockList` and `GetBlockList` (AZB/blockblob/client.go:212-267). `StageBlock` needs a seekable body so retries can rewind.
- **Block IDs:**
  - Base64 strings of at most 64 bytes before encoding, and all the same length within a blob ([MS:put-block]).
  - **[probe]** Azurite rejects a different length with `400 InvalidBlobOrBlock`, URL-safe or unpadded base64 with `400 InvalidQueryParameterValue`, and a 65-byte ID with `400 OutOfRangeInput`.
  - Use standard, padded base64.
- **Limits** for API 2019-12-12 and later ([MS:put-block]):
  - Blocks up to 4,000 MiB each.
  - 50,000 committed blocks per blob, about 190.7 TiB.
  - 100,000 uncommitted blocks.
  - No minimum block size.
- **Persistence:**
  - Staged blocks are server-side state keyed by blob name and block ID, so another process can commit them.
  - They are garbage-collected after a week without a Put Block or Put Block List ([MS:put-block]).
  - **[probe]** `GetBlockList` lists them; `GetProperties` on a blob with only uncommitted blocks returns `404 BlobNotFound`.
- **Interference:**
  - A Put Block List discards uncommitted blocks it does not name, and a Put Blob discards all of them ([MS:put-block]).
  - Re-staging an ID replaces the earlier block, so retries are idempotent.
  - A missing block fails the commit with `400 InvalidBlockList` (**[probe]**).

### 2.3 Finalising at a content address known only at commit

Block IDs are scoped to one blob name and the flat namespace has no rename, so bytes
staged under a temporary name must be copied server-side.

| Option | Behaviour | Verdict |
|---|---|---|
| Copy Blob (`StartCopyFromURL`) | asynchronous and best-effort; for block blobs it "creates a committed blob of zero length before returning", visible while pending; a failed copy leaves an empty blob; a second copy to the destination gets 409 ([MS:copy-blob]); **[probe]** Azurite always completes it synchronously, so tests never see the pending state | rejected: a zero-length placeholder breaks content-addressed `Exists` |
| Copy Blob From URL (`CopyFromURL`) | synchronous, source ≤ 256 MiB ([MS:copy-blob-from-url]); the page contradicts itself on whether Shared Key authorises a same-account source, which our accounts (no Shared Key) cannot test (section 4, question 2) | usable below 256 MiB once verified on real Azure |
| Put Block From URL (`StageBlockFromURL`) ranges into the final name, then one commit | synchronous per range; the final blob stays invisible until the atomic commit; the source must be public, carry a SAS, or be authorised with `x-ms-copy-source-authorization: Bearer` (version 2020-10-02 and later); Shared Key on the destination does not authorise the source ([MS:put-block-from-url]); maximum range per call is stated as both 100 MiB and 4,000 MiB on the same page; **[cloud]** 4,000 MiB is accepted and 4,001 MiB refused with `CannotVerifyCopySource` (2026-10-13 and 2026-10-16); **[probe]** Azurite 3.37.0 returns `403 CannotVerifyCopySource` without a source SAS and succeeds with one | **chosen**: ranges of up to 4,000 MiB (the limit the real service enforces, §4) in parallel with `SourceIfMatch` on the temporary blob's ETag |
| Pointer blob or metadata | no copy, but doubles round trips on every read and needs its own garbage collection | rejected |
| Hierarchical-namespace rename (`x-ms-rename-source`) | atomic, but needs the azdatalake SDK and a DFS endpoint ([path-create]) | possible later fast path |

A declared digest avoids the copy entirely: stage on the final name and commit with
`If-None-Match: *`.

### 2.4 Writes, reads and errors

- **Small blobs:**
  - `blockblob.Client.Upload` is one Put Blob, up to 5,000 MiB (AZB/blockblob/client.go:177; [MS:put-blob]).
  - The `UploadBuffer` and `UploadStream` helpers use random block IDs and cannot resume (AZB/blockblob/client.go:478-482).
  - Put Blob stores a service-computed Content-MD5. A Put Block List stores the `x-ms-blob-content-md5` it is given without checking it.
- **Create if absent:**
  - Use `IfNoneMatch: azcore.ETagAny`.
  - **[probe]** Azurite answers an existing name with `409 BlobAlreadyExists`; the generic documentation says 412 ([conditional headers]). Treat both as "exists".
  - **[probe]** Azurite also answers 409 for a name holding only uncommitted blocks.
  - **[cloud]** Real Azure accepts that write and stores the blob (2026-10-13 and 2026-10-16, section 4).
- **Ranged reads:**
  - `DownloadStream` with `HTTPRange{Offset, Count}`, where Count 0 means "to the end" (AZB/blob/client.go:514; models.go:78-81).
  - `NewRetryReader` re-reads with `If-Match` on the first ETag (blob/retry_reader.go:30-55).
  - **[probe]** An offset at or past the end returns `416 InvalidRange`.
- **Error codes:**
  - `bloberror.HasCode` unwraps `*azcore.ResponseError` and reads `x-ms-error-code` first, so bodiless HEAD responses carry a code (AZB/bloberror/error_codes.go:15-28; AZC/internal/exported/response_error.go:20-24).
  - The codes that matter ([blob-service-error-codes]): `BlobNotFound` and `ContainerNotFound` (404); `BlobAlreadyExists`, `BlockCountExceedsLimit` and `PendingCopyOperation` (409); `InvalidBlockList` and `InvalidBlockId` (400); `CannotVerifyCopySource`.
  - **[probe]** A missing container returns `404 ContainerNotFound`. It is a configuration error, not a missing blob.
- **Listing:**
  - `NewListBlobsFlatPager` with Prefix and MaxResults (up to 5,000); pages may be short and still continue (AZB/container/client.go:256-300; [MS:list-blobs]).
  - Uncommitted-only blobs appear only with `Include.UncommittedBlobs`.
- **Deleting:**
  - `BlobNotFound` on delete counts as success.
  - A blob with snapshots needs `DeleteSnapshots: include`.
  - With soft delete or versioning on, data is retained and billed ([MS:delete-blob]).
- **Consistency:** Azure is strongly consistent ([concurrency-manage]).

### 2.5 SAS, authentication and transport

- **Service SAS:**
  - `sas.BlobSignatureValues{...}.SignWithSharedKey(cred)` (AZB/sas/service.go:104-200).
  - `blob.Client.GetSASURL` re-parses the URL and treats a non-IP host such as `localhost` as path-style, signing the wrong blob name. **[probe]** That gives 403 against `localhost` and 200 against `127.0.0.1`.
  - Sign with explicit container and blob names instead (AZB/internal/shared/shared.go:232-246).
- **User delegation SAS:**
  - `service.Client.GetUserDelegationCredential` with an Entra ID token; keys last at most 7 days ([create-user-delegation-sas]).
  - It derives the account name from the first DNS label of the host (AZB/service/client.go:128).
- **SAS start time:** JFrog backdates it by 900 s (`sasStartTimeOffsetSeconds`).
- **Constructors:**
  - `NewClientWithSharedKeyCredential`, `NewClient` (token credential), `NewClientWithNoCredential` (SAS or anonymous) and `NewClientFromConnectionString` (no `UseDevelopmentStorage=true` shortcut) (AZB/client.go:30-74; internal/shared/shared.go:68-133).
  - Bearer tokens over plain HTTP need `InsecureAllowCredentialWithHTTP` (AZC/policy/policy.go:39-42).
- **Entra ID:**
  - `azidentity.NewDefaultAzureCredential` tries environment, workload identity, managed identity and the developer tools (AZI/default_azure_credential.go:62-90).
  - Managed identity (managed_identity_credential.go:86-113) and workload identity (workload_identity.go:74-93) can also be selected directly.
  - The token scope is `https://storage.azure.com/.default` (AZB/internal/shared/shared.go:22).
- **Retries:** `policy.RetryOptions` defaults to 3 retries, retrying 408, 429 and 5xx. `MaxRetries < 0` disables retries (AZC/policy/policy.go:89-131).
- **Transport:** `ClientOptions.Transport` takes any HTTP doer, which allows fault injection.

### 2.6 Emulator

- **Image:** Azurite v3.37.0, `mcr.microsoft.com/azure-storage/azurite@sha256:830430c1da1a2d537e08f3e6764dd1f5ae00cf0346bcaf625b968ec3f0971fd5` (amd64 and arm64).
- **API version check:** its baseline API version is 2026-06-06, older than the SDK's.
  - **[probe]** Every request is refused with `400 InvalidHeaderValue` unless Azurite runs with `--skipApiVersionCheck` (AZT/src/blob/utils/constants.ts:5).
- **testcontainers module:** `modules/azure/azurite` v0.44.0. It has no default image, `WithEnabledServices(BlobService)`, and `BlobServiceURL` without the account name; it has no connection-string helper.
- **Where Azurite differs from Azure** (AZT/src/blob/persistence/LokiBlobMetadataStore.ts:1047-1075,1998; handlers/BlobHandler.ts:877-879):
  - It never checks the source authorisation of a same-account Copy Blob From URL.
  - It completes Copy Blob synchronously.
  - It enforces neither the block limits nor the 7-day garbage collection.
  - Put Blob does not discard uncommitted blocks.
- **Consequence:** a fake must model real Azure, and Azurite checks the wiring.

### 2.7 JFrog's Azure provider

`azure-blob-storage-v2` ([azure-blob-storage-v2]) takes these parameters:

| Parameter | Default |
|---|---|
| `accountName` | |
| `accountKey` or `sasToken` | |
| `endpoint` | |
| `container` | |
| `path` | `data` |
| `httpsOnly` | true |
| `testConnection` | |
| `enableSignedUrlRedirect` | |
| `signedUrlExpirySeconds` | 30 |
| `multiPartLimit` | 256 MiB |
| `multipartElementSize` | 100 MiB |
| `maxConnections` | 5 |
| `sasStartTimeOffsetSeconds` | 900, from 7.146.35, against intermittent 403s from clock skew |

Managed identity is supported through `useInstanceCredentials` for AKS Workload Identity
from 7.125.4 ([azure-workload-identity]).

### 2.8 Traps

- **Block ID length:** IDs keep one length for every session that ever stages on a name.
- **Commit conflicts:** a concurrent writer to the same final name discards our uncommitted blocks. On `InvalidBlockList`, `BlobAlreadyExists` or `ConditionNotMet` at commit, check whether the final blob exists with the expected size.
- **No overwrites:** never overwrite a committed content blob. An overwrite changes its ETag and breaks in-flight readers pinned with `If-Match`.
- **Accounts without Shared Key:** where Shared Key is disallowed, service and account SAS are rejected. Copy-source authorisation and presigning then need a bearer token or a user delegation SAS.
- **Hierarchical-namespace accounts:** the block APIs work, but listing returns directory entries, and empty directories remain after deletes ([data-lake-storage-known-issues]).

## 3. Mirroring blobs across stores, clouds and regions

### 3.1 How Artifactory does it

Artifactory spreads binaries over several storage units with the `sharding` binary provider:

| Parameter | Default | Meaning |
|---|---|---|
| `redundancy` | 1 (2 on `sharding-cluster`) | copies kept per binary; must not exceed the number of mounts [sharding] [sharding-cluster] |
| `lenientLimit` | 0, meaning "use redundancy" (`sharding-cluster`: 1) | minimum shards that must be available for uploads to continue; the write must reach every available shard and a later balance restores redundancy [sharding] |
| `readBehavior` | roundRobin; `zone`; `crossNetworkStrategy` on clusters | how reads pick a shard [sharding] [sharding-cluster] |
| `writeBehavior` | roundRobin; also freeSpace, percentageFreeSpace, zone variants | where new copies go [sharding] |
| `concurrentStreamWaitTimeout` | 30,000 ms | one upload stream is buffered and fanned out to R writer threads; the write counts when all finish [sharding] |
| `maxBalancingRunTime`, `balancerThreads`, `enableShardingBalancer` | 0.0 ms (no limit), 1, `true` | the balancer that restores redundancy and its off switch (7.63.14) [sharding] |
| `accessibleCheckThreads`, `accessibleCheckTimeoutMillis` | | the shard health check (7.55.13); 7.111.4 exports it as `jfsh_shard_accessibility_status_total` (1 accessible, 0 not, -1 timed out) [sharding] |
| sub-provider `zone`, `checkPeriod` (15 s), `writeEnabled` (since 6.18) | | a shard's zone, its health re-check interval, and a read-only drain switch that still lets GC delete [state-aware] [state-aware-s3] |
| `shared.node.crossZoneOrder` | | the order zones are written in [sharding] |

**Cloud sharding.**
- `s3-sharding` (Artifactory 7.31.10) shards across several S3 buckets, one `state-aware-s3` per bucket. JFrog's own example spans us-east-1 and us-west-1 [s3-templates].
- The GCS and Azure "cluster" templates shard across cluster nodes in front of one bucket or container [gcs-cluster] [azure-cluster].
- No documented chain mixes S3, GCS and Azure; `state-aware-s3` "will always be used with S3 sharding" [state-aware-s3].

**Weaknesses.**
- **No copy index.** "Artifactory does not know which shards hold your binaries" [filestore-whitepaper], so reads and repair probe the shards.
- **Repair waits for garbage collection.** Missing copies were restored only by balancing sessions started after a full garbage collection, about every 80 hours by default, until 7.104.5 made `POST /api/system/storage/optimize` start one at once [balancing] [optimize].
- **Topology changes apply to new data only** on S3 shards [s3-templates].
- **Automatic removal of surplus copies lost data.** It deleted data when combined with bucket replication (KB 000006410) [kb-6410].
- **Storage summary:** `GET /api/storageinfo` has no per-shard detail [storageinfo].
- **Signed URLs:** a redirect signs against the shards in the sharding provider's zone read order. 7.84.10 fixed `enableSignedUrlRedirect` on `state-aware-s3` to follow that order (RTDEV-39396) [rtdev-39396].

**Related features copy between instances, not inside one filestore.** These are
repository replication, federated repositories and Edge. JFrog advises against running
its replication on top of S3 cross-region replication [replication].

### 3.2 Cloud-native replication

| Feature | Behaviour | Fit |
|---|---|---|
| S3 Cross-Region and Same-Region Replication | asynchronous; versioning on both buckets; existing objects need Batch Replication; RTC targets 15 minutes [s3-replication] [s3-rtc] | a member may be a replication source; its replica never counts as one of our copies |
| S3 Multi-Region Access Points | route to the nearest bucket without knowing where an object is, so a read can miss while replication lags; CopyObject only as a destination [s3-mrap] | regional buckets work better as separate members |
| GCS dual-region and multi-region buckets | strongly consistent everywhere; asynchronous geo replication, turbo replication with a 15-minute RPO on dual-region [gcs-availability] [gcs-consistency] | a dual-region bucket is one member with geo redundancy built in |
| Azure GRS, RA-GRS, GZRS, object replication | asynchronous to a fixed paired region or to at most two destination accounts; object-replication destinations reject writes [azure-redundancy] [azure-object-replication] | an RA-GZRS account is one member; a replication destination cannot be a writable member |

None of these replicates across clouds.

### 3.3 Go concerns for parallel writes

- **Fan-out:**
  - `io.MultiWriter` writes to each writer in turn and stops at the first error.
  - `io.Pipe` has no buffer, so the slowest member gates all of them (Go `io/multi.go`, `io/pipe.go`).
  - Handing one immutable part-sized buffer to each member's `Append` in parallel avoids both problems.
- **Quorum:** `errgroup.WithContext` cancels its context on the first error and on `Wait`. A quorum write must not cancel healthy members, and a winning lazy read must not lose its context (`golang.org/x/sync/errgroup`). Per-member contexts and a results channel fit better.
- **Libraries:** `errors.Join` replaces multierror libraries. A per-member circuit breaker is small enough to write rather than import.

## 4. Open questions for a real-cloud check

1. **Azure:** the Put Block From URL maximum range, 100 MiB or 4,000 MiB.
2. **Azure:** whether Copy Blob From URL accepts Shared Key authorisation of a same-account source.
3. **Azure:** whether Put Blob with `If-None-Match: *` succeeds on a name that holds only uncommitted blocks. Azurite returns 409.
4. **GCS:** compose behaviour with a mismatched CRC32C, and with source generation preconditions.
5. **GCS:** whether federated Workload Identity principals without a service account can sign URLs.
6. **S3:** whether a conditional write (`If-None-Match: *`) onto an existing key is refused with 412 by PutObject, CopyObject and CompleteMultipartUpload.
7. **S3:** which checksum an object written with the SDK's defaults carries, and of which type.
8. **S3:** whether a URL presigned for GET is refused for HEAD. The JFrog CLI sends a HEAD before a split download and follows redirects (research [19](19-cdn-and-download-redirect.md) section 2).

Questions 6 to 8 are S3's. They live here because the same workflow asks them; the S3
backend itself predates this report.

Status after the runs below:

| Question | Status |
|---|---|
| 1 | Answered: 4,000 MiB |
| 2 | Open by design: the accounts allow no Shared Key, and the backend never relies on it |
| 3 | Answered: real Azure accepts the write |
| 4 | Answered: a wrong CRC32C is ignored; a stale source generation is 404; an existing destination is 412 |
| 5 | Answered for a federated principal that acts as a service account; a federated principal that acts as none is still untested |
| 6 | Answered: 412 on all three |
| 7 | Answered: a full-object CRC32 |
| 8 | Answered: HEAD is refused with 403 |

The opt-in `cloud-conformance.yml` workflow (issue #12, see
[CI](../testing/ci.md#real-cloud-conformance-manual)) runs the blob contract against real
accounts and logs one `PROBE` line per question:

| Question | Probe |
|---|---|
| 1 | `azure.put_block_from_url.101MiB_range` |
| 2 | `azure.copy_from_url.shared_key_source_without_sas` |
| 3 | `azure.put_blob_if_none_match_over_uncommitted` |
| 4 | `gcs.compose.wrong_crc32c`, `gcs.compose.stale_source_generation`, `gcs.compose.destination_exists` |
| 5 | `gcs.signed_url` |
| 6 | `s3.put.if_none_match_existing`, `s3.copy.if_none_match_existing`, `s3.complete_multipart.if_none_match_existing` |
| 7 | `s3.default_checksum` |
| 8 | `s3.presigned_get` |

The backends do not depend on the answers:
- The Azure backend copied in 100 MiB ranges; it now uses 4,000 MiB, the verified limit.
- It always authorises copy sources explicitly, with a service SAS or an Entra bearer token.
- It treats `BlobAlreadyExists` and `ConditionNotMet` alike on a must-not-exist write.
- The GCS backend checks the composed size and CRC32C itself.
- The S3 backend checks for an existing content key with HeadObject before it writes, and never presigns HEAD.

The answers record the real behaviour and show whether those defensive choices can be
relaxed.

### GCS answers (2026-10-13)

The blob contract passed in full (17 subtests) against a real bucket in `europe-west2`
(`deploy/terraform/acceptance` at the time, now `deploy/platform/terraform/gcp/single-bucket`),
with signed URLs fetched over HTTPS. The run acted as the acceptance service account by
impersonation, because the organisation enforces
`iam.disableServiceAccountKeyCreation`, so no key was involved.

| Question | Answer | Consequence |
|---|---|---|
| 4: compose with a wrong CRC32C | Accepted: compose ignores a supplied CRC32C, as the JSON API documentation says. The SDK comment claiming it is rejected is wrong | The backend's own size and CRC32C check after compose is required, not merely defensive |
| 4: stale source generation | Rejected with 404 `notFound`, not 412 | Translated to not-found; parts are written must-not-exist, so a stale generation means a lost part and the upload fails |
| 4: destination exists | 412 `conditionNotMet` | Translated to `ErrPrecondition`: the blob is already stored |
| 5: signing without a key | Works through IAM `signBlob` as the service account (`google_access_id`), once the account holds Service Account Token Creator on itself | Keyless installs (impersonation locally, Workload Identity Federation in CI and on GKE) presign; a federated principal that impersonates no service account still cannot sign |
| Composites | No MD5; `componentCount` is reported | As expected: the contract computes its own digests |

A 40 MiB deploy through the local container (`make gcp-smoke`) stored one object composed
from three parts (16, 16 and 8 MiB) and left nothing under `_uploads/`.

### Azure answers (2026-10-13)

The blob contract passed in full (15 subtests) against a real StorageV2 account in
`uksouth` (`deploy/terraform/acceptance-azure` at the time, now
`deploy/platform/terraform/azure/single-bucket`), with Shared Key disabled. It used Entra ID
through `DefaultAzureCredential`, and presigned URLs from user delegation keys fetched over
HTTPS.

| Question | Answer | Consequence |
|---|---|---|
| 1: Put Block From URL range | 4,000 MiB, the limit the documentation also states: ranges of 100, 101, 256 and 1,024 MiB and 4,000 MiB are accepted (the last in about 14 s server-side), and 4,001 MiB is refused with `CannotVerifyCopySource`. The probe assembles a 4,100 MiB source server-side, so it uploads nothing more than 101 MiB (`ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES=1`) | `copy_range_size` now defaults to 4,000 MiB: a commit copies up to about 3.9 GiB per call, so a 10 GiB blob takes three calls instead of 103 |
| 2: Copy Blob From URL with a Shared Key source | Not asked: an account without Shared Key can't answer it | Moot, because the backend always authorises a copy source explicitly: a service SAS, a user delegation SAS, the configured SAS, or `x-ms-copy-source-authorization` under Entra ID |
| 3: Put Blob with `If-None-Match: *` over uncommitted blocks | Accepted: real Azure stores the blob, where Azurite answers 409 | A must-not-exist small-blob write isn't blocked by blocks a crashed upload left staged under that name |

Running the backend on a container SAS (`blob.azure.credential: sas`, the local container's
mode) found a bug: the copy source carried the SAS twice, and every copy into place failed
with `CannotVerifyCopySource`. It's fixed in `sourceURL`. A deploy without a declared
digest stages into a temporary blob and copies it into place, so the 40 MiB smoke blob was
committed as one block copied by a single range.

### Confirmed in CI (2026-10-16)

After the reference-deployment rework (epic #189), `cloud-conformance.yml` ran once per
cloud against a freshly applied `single-bucket` scenario, each destroyed afterwards. The
blob contract passed on all three. Unlike the operator runs above, each job authenticated
as CI does, through OpenID Connect with no stored secret: on GCP, Workload Identity
Federation acting as the conformance service account (`google-github-actions/auth` with
`workload_identity_provider` and `service_account`); on Azure, `azure/login` as the
data-plane managed identity; on AWS, the assumed data-plane role.

| Cloud | Run | Probe results |
|---|---|---|
| GCS | [38891476745](https://github.com/weaveplatform/artifactweave/actions/runs/38891476745) | `compose.wrong_crc32c rejected=false`; `compose.stale_source_generation rejected=true` (404 `notFound`); `compose.destination_exists precondition_failed=true` (412 `conditionNotMet`); `signed_url ok=true`; `compose.md5_present=false component_count=2` |
| Azure | [38892778296](https://github.com/weaveplatform/artifactweave/actions/runs/38892778296) | `put_block_from_url` accepted at 100, 101, 256 (1.3 s), 1,024 (4.3 s) and 4,000 MiB (16.5 s), refused at 4,001 MiB with `CannotVerifyCopySource`; `put_blob_if_none_match_over_uncommitted accepted=true`; `copy_from_url.shared_key_source_without_sas skipped=true` (the account allows no Shared Key) |
| S3 | [38892387927](https://github.com/weaveplatform/artifactweave/actions/runs/38892387927) | see the S3 answers below |

The GCS and Azure answers match those of 2026-10-13. What the GCS signing probe proves is
narrower than question 5 as asked: a job federated through Workload Identity Federation
that acts as a service account signs V4 URLs through IAM `signBlob` as that account, as
the 2026-10-13 consequence predicted for CI. A federated principal that acts as no
service account was not tested and still cannot sign.

### S3 answers (2026-10-16)

From `TestRealS3Probes` (`pkg/blob/s3/real_test.go`) in run 38892387927, against the
`aws/single-bucket` conformance bucket:

| Question | Answer | Consequence |
|---|---|---|
| 6: conditional writes onto an existing key | PutObject, CopyObject and CompleteMultipartUpload with `If-None-Match: *` all answer 412 `PreconditionFailed` | S3 enforces must-not-exist itself, as GCS and Azure do. The backend now writes every content key conditionally instead of checking with HeadObject first (decision [0027](decisions/0027-cloud-blob-backends.md), #255) |
| 7: default checksum | `FULL_OBJECT` CRC32; no CRC64NVME | Informational: the contract computes its own SHA-256 |
| 8: a URL presigned for GET, used for HEAD | HEAD answers 403, GET answers 200 | Confirms the inference in research [19](19-cdn-and-download-redirect.md) section 2 for S3: HEAD and conditional requests on a redirecting repository are answered locally and never redirected, as decision [0029](decisions/0029-cdn-delivery-and-download-redirect.md) requires |

**[probe]** MinIO, the image `pkg/containertest` pins (2026-10-16), refuses `If-None-Match: *` onto an existing key with 412 on PutObject and CompleteMultipartUpload, but ignores it on CopyObject and overwrites the destination. Content keys make that harmless: the copy writes the same bytes.

## Sources

**Google Cloud Storage**
- [resumable-uploads]: https://docs.cloud.google.com/storage/docs/resumable-uploads
- [performing-resumable-uploads]: https://docs.cloud.google.com/storage/docs/performing-resumable-uploads
- [rapid-bucket]: https://docs.cloud.google.com/storage/docs/rapid/rapid-bucket
- [multipart-uploads]: https://docs.cloud.google.com/storage/docs/multipart-uploads
- [request-preconditions]: https://docs.cloud.google.com/storage/docs/request-preconditions
- [quotas]: https://docs.cloud.google.com/storage/quotas
- [compose reference]: https://docs.cloud.google.com/storage/docs/json_api/v1/objects/compose
- [composite-objects]: https://docs.cloud.google.com/storage/docs/composite-objects
- [consistency]: https://docs.cloud.google.com/storage/docs/consistency
- [soft-delete]: https://docs.cloud.google.com/storage/docs/soft-delete
- [lifecycle]: https://docs.cloud.google.com/storage/docs/lifecycle
- [pricing]: https://cloud.google.com/storage/pricing
- [google-storage-v2]: https://docs.jfrog.com/installation/docs/google-storage-binary-provider-native-client-template

**Mirroring (Artifactory and cloud replication)**
- [sharding]: https://docs.jfrog.com/installation/docs/configure-a-sharding-binary-provider
- [sharding-cluster]: https://docs.jfrog.com/installation/docs/sharding-cluster-binary-provider
- [state-aware]: https://docs.jfrog.com/installation/docs/state-aware-binary-provider
- [state-aware-s3]: https://docs.jfrog.com/installation/docs/state-aware-s3-binary-provider
- [s3-templates]: https://docs.jfrog.com/installation/docs/s3-binary-storage-templates
- [gcs-cluster]: https://docs.jfrog.com/installation/docs/google-storage-v2-cluster-binary-provider
- [azure-cluster]: https://docs.jfrog.com/installation/docs/azure-blob-storage-cluster-v2-binary-provider
- [balancing]: https://docs.jfrog.com/installation/docs/using-balancing-to-recover-from-mount-failure
- [optimize]: https://docs.jfrog.com/artifactory/reference/optimizesystemstorage
- [storageinfo]: https://docs.jfrog.com/artifactory/reference/getstoragesummaryinfo
- [filestore-whitepaper]: https://jfrog.com/whitepaper/best-practices-for-managing-your-artifactory-filestore/
- [kb-6410]: https://jfrog.com/help/r/artifactory-s3-bucket-content-deleted-by-artifactory-from-backend/artifactory-s3-bucket-content-deleted-by-artifactory-from-backend
- [replication]: https://docs.jfrog.com/artifactory/docs/repository-replication
- [s3-replication]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication.html
- [s3-rtc]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication-time-control.html
- [s3-mrap]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/MultiRegionAccessPointRequestRouting.html
- [gcs-availability]: https://docs.cloud.google.com/storage/docs/availability-durability
- [gcs-consistency]: https://docs.cloud.google.com/storage/docs/consistency
- [azure-redundancy]: https://learn.microsoft.com/en-us/azure/storage/common/storage-redundancy
- [azure-object-replication]: https://learn.microsoft.com/en-us/azure/storage/blobs/object-replication-overview

**Azure Blob Storage**
- [MS:put-block]: https://learn.microsoft.com/en-us/rest/api/storageservices/put-block
- [MS:put-blob]: https://learn.microsoft.com/en-us/rest/api/storageservices/put-blob
- [MS:put-block-from-url]: https://learn.microsoft.com/en-us/rest/api/storageservices/put-block-from-url
- [MS:copy-blob]: https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob
- [MS:copy-blob-from-url]: https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob-from-url
- [MS:list-blobs]: https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs
- [MS:delete-blob]: https://learn.microsoft.com/en-us/rest/api/storageservices/delete-blob
- [conditional headers]: https://learn.microsoft.com/en-us/rest/api/storageservices/specifying-conditional-headers-for-blob-service-operations
- [blob-service-error-codes]: https://learn.microsoft.com/en-us/rest/api/storageservices/blob-service-error-codes
- [create-user-delegation-sas]: https://learn.microsoft.com/en-us/rest/api/storageservices/create-user-delegation-sas
- [path-create]: https://learn.microsoft.com/en-us/rest/api/storageservices/datalakestoragegen2/path/create
- [concurrency-manage]: https://learn.microsoft.com/en-us/azure/storage/blobs/concurrency-manage
- [data-lake-storage-known-issues]: https://learn.microsoft.com/en-us/azure/storage/blobs/data-lake-storage-known-issues
- [azure-blob-storage-v2]: https://docs.jfrog.com/installation/docs/azure-blob-storage-v2-binary-provider
- [azure-workload-identity]: https://docs.jfrog.com/installation/docs/azure-workload-identity
- [rtdev-39396]: https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed

## Artifactory 7.x check

Checked on 2026-10-17 against the self-managed release notes up to 7.161.32
(https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases), the legacy notes
for 7.0 to 7.83 (https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf) and the current binary-provider pages cited above. Sections
1.6, 2.7 and 3.1 were corrected in place.

| Claim | 7.x | Source |
|---|---|---|
| `google-storage-v2` parameters | Confirmed; `retryIntervalMillis` added beside `maxRetries` (7.96.3, improved in 7.104.5) | [google-storage-v2], https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed |
| `azure-blob-storage-v2` parameters and Workload Identity | Confirmed; `sasStartTimeOffsetSeconds` arrived in 7.146.35, Workload Identity in 7.125.4 | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714635-self-managed, https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed |
| Sharding parameters (`redundancy`, `lenientLimit`, `readBehavior`, `concurrentStreamWaitTimeout`) | Confirmed | [sharding] |
| Balancing | Confirmed; `enableShardingBalancer` (7.63.14) and the `maxBalancingRunTime` default of 0.0 ms added | [sharding], [legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf) |
| `writeEnabled` on state-aware shards, "since 6.18" | Confirmed for 7.x: still documented, default `true` | [state-aware] |
| Repair waits for garbage collection until 7.104.5 | Confirmed | [optimize] |
| Which shard a signed URL redirect uses | Changed: the zone read order of the sharding provider (7.84.10 fix) | [rtdev-39396] |
| S3 storage | Added: the S3 provider uses AWS SDK v2 by default from 7.146.7 (`awsSdkV2`), except with KMS client-side encryption, which stays on SDK v1 (7.146.22) | https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed, https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714622-self-managed |
| `GET /api/storageinfo` has no per-shard detail | Confirmed for the API; per-shard health is a metric (7.111.4) | [storageinfo], https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed |
