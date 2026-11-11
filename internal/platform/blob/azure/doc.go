// Package azure is the Azure Blob Storage blob store (decision 0027). Blobs
// live at <prefix>sha256/<ab>/<hex> in one container.
//
// Azure block blobs are written as staged blocks that a later Put Block List
// commits, and staged blocks are server-side state any process can commit, so
// an upload session maps onto them directly:
//
//   - Appended bytes accumulate in a pending buffer of at most PartSize; each
//     full part is staged as one block. A blob that never fills a part is
//     written with a single Put Blob at commit.
//   - Block IDs are derived from the upload ID and the block number, so the
//     session only records how many blocks it staged, and re-staging a block
//     after a failure is idempotent.
//   - Staged blocks belong to one blob name and the namespace has no rename.
//     When the client declared the digest up front the blocks are staged on
//     the content key and commit needs no copy. Otherwise they are staged on
//     a temporary blob, which commit finalises and then copies into the
//     content key with parallel Put Block From URL ranges and one atomic
//     Put Block List. Asynchronous Copy Blob is never used: it shows a
//     zero-length blob at the destination while it runs, which would answer
//     Exists for content that is not there.
//   - Every commit to a content key carries If-None-Match: *, so a committed
//     blob is never overwritten; losing that race to an identical upload is
//     success.
//
// # Role
//
// platform layer, adapter: the backend for one object store, an Azure Blob
// Storage container, implementing platform/blob's Store. Blob storage
// belongs to no module, so the backends sit in the platform below the blob
// port. The composition root builds one from the blob.azure configuration
// for the hot or the cold tier, or for an azure member of a blob mirror.
// Tests outside the package build it on Azurite in the mirror's cross-cloud
// scenario, and the platform scenario suite's Front Door checks, in the
// azure/cdn scenario, use NewAPI and ReadSAS to sign as the deployer and to
// probe the storage account. It imports platform/blob,
// platform/blob/session, platform/cdn, the standard library and the Azure
// SDK for Go.
//
// # Inputs
//
//   - ClientConfig, which mirrors the blob.azure section of the
//     configuration: the Blob service URL (empty for the account's public
//     endpoint), the container, which must exist, and the account; exactly
//     one way to authenticate, taken in the order connection string, account
//     key, pre-issued SAS token, an Entra ID credential, and otherwise a
//     managed identity (ManagedIdentityClientID) or DefaultAzureCredential;
//     how far SAS start times are backdated, 15 minutes when zero; and the
//     SDK pipeline options. NewAPI builds the API from it without a request.
//     CanSign reads the configuration alone to tell whether the client can
//     sign read URLs: a pre-issued SAS token cannot.
//   - Options for New: the blob name prefix; the block size, DefaultPartSize
//     when zero; the range each Put Block From URL copies,
//     DefaultCopyRangeSize when zero, both at most 4,000 MiB; the copy
//     concurrency, DefaultConcurrency when zero; the session.Store that keeps
//     sessions between requests (the composition root passes
//     session.InUploads under "azure:<tier>", such as "azure:blob", or
//     "azure:<tier>.<member>" for a mirror member); Presign, which the
//     composition root sets from CanSign; a cdn.FrontDoor, with which a store
//     that signs also signs CDN URLs; and the clock CDN windows are cut from.
//   - What platform/blob's Store and Upload take: upload options, the bytes
//     appended, declared digests, upload IDs, the SHA-256 of a stored blob
//     and, to sign a URL, its blob.SignOptions.
//
// # Outputs
//
//   - Store, from New: a blob.Store that is also a blob.Signer, of read SAS
//     URLs on storage when Presign is set and of Front Door URLs when it
//     also has a Front Door, and a blob.PartSizer. ObjectKey gives the blob
//     name, within the container, that a CDN signer addresses.
//   - API, the container operations the store calls, with Props, Item and
//     ReadSAS. NewAPI implements it over the SDK, and tests substitute a
//     fake.
//   - ClientConfig with CanSign, DefaultPartSize, DefaultCopyRangeSize,
//     DefaultConcurrency, ErrPrecondition (an If-None-Match or If-Match
//     condition failed), ErrBlockList (a block list named a block that is not
//     staged), ErrTooManyBlocks (an upload needs more than Azure's 50,000
//     blocks), ErrConfig, which every configuration error and a missing
//     container wrap, and ErrSeek.
//
// # Invariants
//
//   - A blob is stored under its lowercase hex SHA-256 and is committed
//     under that name only after Commit has verified the digest, including
//     blocks staged there because the digest was declared. Walk lists only
//     <prefix>sha256/.
//   - A block ID is the standard base64 of the upload ID, '-' for a staged
//     block or '+' for a copy block, and a six-digit block number: 39 bytes
//     before base64. The format is frozen, because Azure requires every
//     block ID of one blob to have the same length, and blocks that
//     different sessions stage on one content key share that blob.
//   - A failed condition at commit, or a block list whose blocks a
//     concurrent commit discarded, is success only once the stored blob has
//     the size that was written; any other size fails the commit.
//   - Parts are buffered through platform/blob/session, and the session is
//     saved after every append. Its document holds the name the blocks are
//     staged on, how many there are, the phase and the ETag of the committed
//     temporary blob; the json tags of state and the phase values, "" while
//     staging and "copying" once the temporary blob is committed, are the
//     storage format, because a session saved by one process is resumed by
//     another. A retried Commit in the copying phase resumes the copy.
//   - The copy reads the temporary blob pinned to its ETag, at most
//     Concurrency ranges at once, and starts no new range after a failure.
//     Its source is authorised with a service SAS under a shared key, the
//     container's own SAS token, or an Entra ID bearer token.
//   - Abort deletes the temporary blob. Blocks staged on a content key are
//     left for Azure to discard after a week, because deleting that name
//     could remove another session's blocks or a committed blob.
//   - A missing blob is blob.ErrNotFound, and a missing container is
//     ErrConfig, so a misconfigured container never looks like an empty
//     store.
//   - A reader is pinned to the blob's ETag and opens it only when read and
//     again after a Seek, so the seek-to-end probe of http.ServeContent
//     costs no request. A read at the end returns io.EOF without a request,
//     because Azure answers a range past the end with 416.
//   - A storage URL is a read SAS for one blob, named explicitly, carrying
//     the file name and media type as rscd and rsct, with its start
//     backdated against clock skew: a service SAS under a shared key, or a
//     user delegation SAS under Entra ID, whose key is fetched once and
//     renewed when a SAS would expire within an hour of it. A SAS that the
//     longest key the service issues could not cover is refused rather than
//     signed to fail at storage. A store on a pre-issued SAS token signs
//     nothing.
//   - A CDN URL is a read SAS fixed to the current TTL-long window, with
//     rscc set to "public, max-age=<TTL>", placed on the Front Door
//     endpoint, so every client in one window gets the same URL and an edge
//     copy lasts at most the TTL (decision 0029). Front Door checks no
//     signature of its own, so there is no CDN route without a SAS the store
//     can mint. The store is never wrapped, so it keeps PartSizer, and New
//     refuses a Front Door whose origin path does not reach the container
//     and prefix.
//   - Errors the store creates start "blob/azure:", and those of
//     platform/blob pass through unwrapped, because their texts already name
//     their package. Every text stays byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Blobs": content addressing and upload sessions that resume across
//     requests).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     ("Shared session layer", and "azure: staged blocks with a server-side
//     copy only when the digest was not declared").
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md (an
//     azure member of a mirror, whose chunks follow the member's part size).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Signing contract": read SAS URLs, and Front Door, which checks no
//     signature and passes the SAS through to storage).
//   - Decision 0030: docs/research/decisions/0030-reference-deployments.md
//     (the azure/single-bucket scenario that the real-cloud run uses, with
//     its conformance/ prefix, and the azure/cdn scenario that the platform
//     suite's Front Door checks run in).
//   - Research 18, section 2 (Azure Blob Storage) and section 4, "Azure
//     answers (2026-10-13)": docs/research/18-cloud-blob-storage.md.
//   - The cloud conformance workflow, .github/workflows/cloud-conformance.yml
//     (job azure), and docs/testing/ci.md#real-cloud-conformance-manual.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 147 (platform/blob/azure) and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2b-cloud-blob-storage-and-mirroring,
//     #p4b-reference-deployments-and-cdn-delivery and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/10
//   - https://github.com/weaveplatform/artifactweave/issues/983
//
// # Status
//
// Implemented P2b: the store, with blocks staged on the content key when
// the digest is declared and otherwise on a temporary blob copied into
// place with parallel Put Block From URL ranges, and presigned GETs as a
// service or user delegation SAS (issue 10, pull request 31), the
// real-cloud conformance run (issue 12) and PartSize, by which the mirror
// sizes its chunks (issue 39). The keyless real-cloud environment (pull
// request 88) found that a copy source on a SAS token carried the SAS
// twice, which it fixed, and raised the default copy range from 100 MiB to
// 4,000 MiB, the most the real service accepts. P4b replaced the presigned
// GET with blob.Signer, whose read SASs carry the file name and media type,
// together with CanSign (issue 119), and added Front Door URLs, a window's
// SAS on the endpoint (issue 122). Moved here in P9d.
//
// # Testing
//
//   - TestAccContractOnFake and TestAccContractWithDurableSessions: the
//     blobtest contract, with 32 KiB parts and 48 KiB copy ranges, on an
//     in-memory fake of Azure that enforces what Azurite does not (block ID
//     rules, uncommitted blocks discarded by a commit that does not name
//     them, If-None-Match on committed blobs, an authorised copy source with
//     the expected ETag, the block limit), the second with its sessions kept
//     in the uploads table of the platform's in-memory store.
//     TestUnitFakeModelsAzure checks the fake's own rules.
//   - TestAccContractOnAzurite: the contract through the SDK adapter on
//     Azurite, with small parts and copy ranges so blocks, the temporary
//     commit and the parallel copy all run, presigned URLs fetched over HTTP,
//     and CDN URLs served through cdntest's Front Door edge with Azurite as
//     its origin, which checks each SAS. It needs a Docker engine that runs
//     Linux containers and skips without one.
//   - TestUnitBlockIDs, TestUnitUndeclaredCommitCopiesInRanges,
//     TestUnitDeclaredDigestSkipsTheCopy, TestUnitSmallBlobIsOnePutBlob,
//     TestUnitConcurrentIdenticalCommits, TestUnitCommitRetryResumesTheCopy,
//     TestUnitBlockLimit and TestUnitAbort: the 39-byte block IDs, each
//     commit path, losing the race to an identical upload on each, a retried
//     commit resuming the copy, the block limit, and an abort that deletes
//     the temporary blob but never a content key.
//   - TestUnitNewValidates, TestUnitAppendFailures, TestUnitCommitFailures,
//     TestUnitResumeWithDamagedState, TestUnitReadErrors,
//     TestUnitDeleteWalkPresignErrors and TestUnitContainerNotFound:
//     configuration refused with ErrConfig, each API call failing through
//     the fake, and a missing container reported as ErrConfig rather than as
//     an empty store.
//   - TestUnitTranslate, TestUnitConnectionKey, TestUnitNewAPIConfigurations,
//     TestUnitCanSign, TestUnitServiceSAS, TestUnitSourceAuthorisation and
//     TestAccDelegationSAS: the SDK adapter's mapping onto the sentinels,
//     each way to authenticate and whether it signs, the service SAS, how a
//     copy source is authorised, and a user delegation key from a chi test
//     server that models Get User Delegation Key, reused.
//   - TestAccFrontDoorURLs, TestAccFrontDoorOriginPath,
//     TestAccFrontDoorNeedsASigner, TestAccServiceSASWindow and
//     TestAccDelegationSASWindow: a CDN URL is a SAS on the endpoint, the
//     same string throughout one window and a new one in the next; an origin
//     path is honoured, and one that cannot reach the store refused; a store
//     that cannot mint a SAS has no CDN route; and a SAS the longest
//     delegation key could not cover is refused.
//   - TestAccContractOnRealAzure and TestAccRealAzureProbes: the contract on
//     a real container, with the Put Block From URL copy under real
//     copy-source authorisation and SAS URLs fetched over HTTPS, which
//     Azurite does not check, and the probes, which answer research 18's open
//     questions as PROBE lines. They skip unless
//     ARTIFACTWEAVE_TEST_AZURE_ACCOUNT and ARTIFACTWEAVE_TEST_AZURE_CONTAINER
//     are set. They authenticate with ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY
//     when it is set and through Entra ID otherwise, so they also run on
//     accounts that allow no Shared Key, and run the large-range probes only
//     with ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES. The cloud conformance
//     workflow dispatches them, and make conformance in the
//     azure/single-bucket scenario runs them locally.
package azure
