// Package s3 is the S3-compatible blob store (AWS S3, MinIO, Ceph RGW and
// similar). Blobs live at <prefix>sha256/<ab>/<hex>.
//
// Object stores cannot append or rename, so an upload session is shaped
// around those limits:
//
//   - Bytes accumulate in a pending buffer of at most PartSize. A blob that
//     never fills one part is written with a single PutObject at commit; most
//     package files are that small, and they never pay for a multipart upload.
//   - When the buffer fills, the session starts an S3 multipart upload (on the
//     first full part) and uploads the part. S3 requires every part except the
//     last to be at least 5 MiB, which is why appends are buffered rather than
//     uploaded as they arrive: Docker clients PATCH in chunks of any size.
//   - The multipart upload ID, the part list, the pending tail and the hash
//     state are saved through a session.Store after every append, so a
//     session resumes in a later request (decision 0027; session.InUploads
//     keeps it in the metadata database).
//   - At commit the content key is known. If the client declared the digest
//     up front the multipart upload already targets the content key; otherwise
//     it targets a temporary key and is copied into place, with a multipart
//     copy above S3's 5 GiB single-copy limit.
//   - Every write of a content key is a must-not-exist write (If-None-Match:
//     *, decision 0027), as the GCS and Azure backends write, so a blob that
//     is already stored is never written again and two uploads of one blob
//     cannot race. S3 answers 412 for a taken key, and the existing object is
//     adopted. There is no HeadObject before the write.
//
// # Role
//
// platform layer, adapter: the backend for one object store, an S3 bucket,
// implementing platform/blob's Store. Blob storage belongs to no module, so
// the backends sit in the platform below the blob port. The composition root
// builds one from the blob.s3 configuration for the hot or the cold tier, or
// for an s3 member of a blob mirror. Tests outside the package build it on
// MinIO: the mirror's cross-cloud scenario, the composition root's runtime
// test and the CloudFront origin of the CDN acceptance feature. It imports
// platform/blob, platform/blob/session, platform/cdn, the standard library
// and the AWS SDK for Go v2.
//
// # Inputs
//
//   - ClientConfig, which mirrors the blob.s3 section of the configuration:
//     the endpoint (empty for AWS), the region, path-style addressing, and
//     static keys or a profile of the shared AWS configuration, otherwise the
//     AWS default chain. NewClient builds the SDK client and its presigner
//     from it without a request.
//   - Options for New: the bucket, which must exist; the key prefix; the part
//     size, DefaultPartSize when zero and refused below S3's 5 MiB; the
//     session.Store that keeps sessions between requests (the composition
//     root passes session.InUploads under "s3:<tier>", such as "s3:blob", or
//     "s3:<tier>.<member>" for a mirror member); the presigner, with which
//     the store signs storage URLs; and a cdn.Signer, such as a
//     cdn.CloudFront, with which it signs CDN URLs.
//   - What platform/blob's Store and Upload take: upload options, the bytes
//     appended, declared digests, upload IDs, the SHA-256 of a stored blob
//     and, to sign a URL, its blob.SignOptions.
//
// # Outputs
//
//   - Store, from New: a blob.Store that is also a blob.Signer, of storage
//     URLs when it has a presigner and of CDN URLs when it has a CDN signer,
//     and a blob.PartSizer. ObjectKey gives the key a CDN signer addresses.
//   - API and PresignAPI, the parts of the SDK's client and presign client
//     that the store calls, so tests substitute a fake.
//   - NewClient and ClientConfig, DefaultPartSize, ErrConfig, which every
//     configuration error wraps, and ErrSeek.
//
// # Invariants
//
//   - A blob is stored under its lowercase hex SHA-256, and an object
//     reaches that key only after Commit has verified the digest. A taken
//     content key is adopted only when the stored object has the size that
//     was written; any other size fails the commit and leaves the object
//     alone.
//   - The condition saves work and races but never guards correctness,
//     because a write over a content key stores the same bytes: a store that
//     ignores it (MinIO, on CopyObject) writes again, and a store that
//     refuses it as NotImplemented is written once more without it and is
//     not asked again.
//   - Parts are buffered through platform/blob/session, and the session is
//     saved after every append. Its document holds the multipart upload's
//     key and ID and each part's number and ETag; the json tags of multipart
//     and part are the storage format, because a session saved by one
//     process is resumed by another.
//   - A missing key is blob.ErrNotFound, and a finished or aborted multipart
//     upload is blob.ErrUploadUnknown.
//   - A store with a CDN signer answers blob.ViaCDN itself and is never
//     wrapped, so it keeps PartSizer (decision 0029); New refuses a signer
//     whose origin path does not contain the prefix. A presigned storage URL
//     lasts at most 7 days, SigV4's limit.
//   - A reader opens the object only when read and again after a Seek, so
//     the seek-to-end probe of http.ServeContent costs no request and a
//     ranged read costs one.
//   - Errors the store creates start "blob/s3:". Those of platform/blob and
//     platform/blob/session pass through unwrapped, because their texts
//     already name their package. Every text stays byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Blobs": content addressing and upload sessions that resume across
//     requests).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     ("Shared session layer", and "s3: must-not-exist writes").
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md (an s3
//     member of a mirror, whose chunks follow the member's part size).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Signing contract": presigned storage URLs and CloudFront).
//   - Decision 0030: docs/research/decisions/0030-reference-deployments.md
//     (the aws/single-bucket scenario that the real-cloud run uses, and its
//     conformance/ prefix).
//   - Research 18, section 4, "S3 answers (2026-10-16)":
//     docs/research/18-cloud-blob-storage.md.
//   - The cloud conformance workflow, .github/workflows/cloud-conformance.yml
//     (job s3), and docs/testing/ci.md#real-cloud-conformance-manual.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 147 (platform/blob/s3) and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 3,
//     #p2b-cloud-blob-storage-and-mirroring,
//     #p4b-reference-deployments-and-cdn-delivery and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/983
//
// # Status
//
// Implemented P2: the store, with multipart uploads whose sessions are kept
// in the uploads table, path-style addressing for MinIO and presigned GETs
// (pull request 3). P2b moved its sessions onto the shared session layer
// (issue 8, pull request 30) and added PartSize, by which the mirror sizes
// its chunks (issue 39). P3 added the shared-configuration profile, so
// stores in one process can use different accounts (issue 42). P4b replaced
// the presigned GET with blob.Signer, whose storage URLs carry the file name
// and media type (issue 119), added CloudFront URLs through a cdn.Signer
// (issue 120) and the real-cloud conformance run (issue 128). On 2026-10-16
// must-not-exist writes replaced the HeadObject before each write (issue
// 255, pull request 312). Staged in P9d.
//
// # Testing
//
//   - TestAccContractOnFake and TestAccContractWithDurableSessions: the
//     blobtest contract on an in-memory fake of S3 with 256 KiB parts, the
//     second with its sessions kept in the uploads table of the in-memory
//     metadata store.
//   - TestAccContractOnMinIO: the contract on MinIO with S3's 5 MiB part
//     floor, so multipart uploads, the copy into place and presigned GETs run
//     end to end. TestAccConcurrentCommitsOnMinIO: sessions commit one blob
//     at once on each write path, all succeed and the blob reads back whole.
//     Both need a Docker engine that runs Linux containers and skip without
//     one.
//   - TestAccNewClientBuildsWithoutNetwork: NewClient applies its
//     configuration, and the store presigns, without a request.
//   - TestUnitCommitWritesConditionallyWithoutAHead,
//     TestUnitCommitAdoptsAStoredBlob, TestUnitCommitAdoptsItsOwnLandedRetry,
//     TestUnitCommitRefusesAStoredObjectOfAnotherSize,
//     TestUnitCommitAdoptCheckFails, TestUnitCommitWithoutConditionalSupport,
//     TestUnitCommitWhereTheConditionIsIgnored and
//     TestUnitConcurrentCommitsOfOneBlob: the must-not-exist writes, each
//     outcome of a taken key, and stores that ignore or refuse the condition.
//   - TestUnitKeysAreContentAddressedUnderPrefix,
//     TestUnitDeclaredDigestSkipsTheCopy and
//     TestUnitMultipartCopyAboveTheCopyLimit: the key layout, a small blob as
//     one PutObject, parts on the content key when the digest is declared,
//     and the multipart copy above the single-copy limit.
//   - TestUnitNewValidates, TestUnitAppendFailures, TestUnitCommitFailures,
//     TestUnitResumeWithDamagedState, TestUnitReadErrors,
//     TestUnitDeleteWalkPresignErrors, TestUnitTranslate and
//     TestUnitCDNSigning: configuration refused with ErrConfig, each SDK call
//     failing through the fake, a damaged session ending as
//     blob.ErrUploadUnknown, the mapping onto the blob sentinels, and CDN
//     URLs for the prefixed key.
//   - TestAccContractOnRealS3 and TestAccRealS3Probes: the contract on a real
//     bucket, with presigned URLs fetched over HTTPS and the copy limits
//     lowered so the multipart copy runs, and the probes, which log what S3
//     answers as PROBE lines. They skip unless ARTIFACTWEAVE_TEST_S3_BUCKET
//     is set. The cloud conformance workflow dispatches them, and make
//     conformance in the aws/single-bucket scenario runs them locally.
package s3
