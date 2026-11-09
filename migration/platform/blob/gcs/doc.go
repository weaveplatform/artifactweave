// Package gcs is the Google Cloud Storage blob store (decision 0027). Blobs
// live at <prefix>sha256/<ab>/<hex>.
//
// GCS objects cannot be appended to, and the Go SDK cannot resume a
// resumable-upload session from persisted state, so an upload session is
// built from compose instead:
//
//   - Bytes accumulate in a pending buffer of at most PartSize. A blob that
//     never fills one part is written in a single request at commit.
//   - Each full part becomes its own object under <prefix>_uploads/<id>/,
//     written with a must-not-exist precondition. Part names are never
//     reused across parts, so a retry of the same part either lands or finds
//     its own earlier copy.
//   - GCS composes at most 32 sources per request, so before a 33rd source
//     the session composes what it has into a fold object and continues from
//     that. There is no limit on how many components a composite object has.
//   - At commit the content key is known; the session composes everything
//     straight onto it with a must-not-exist precondition and
//     DeleteSourceObjects, so the bytes are never copied. A 412 there means
//     the blob is already stored, which the contract treats as success.
//   - Composite objects carry a CRC32C but no MD5, so the session tracks the
//     CRC32C of everything appended and checks the result against it; the
//     blob.Hasher state still provides SHA-256, SHA-1 and MD5.
//
// The session (part objects, their generations, sizes and checksums) is
// persisted through migration/platform/blob/session after every append.
//
// # Role
//
// platform layer, adapter: the backend for one object store, a Google Cloud
// Storage bucket, implementing platform/blob's Store. Blob storage belongs
// to no module, so the backends sit in the platform below the blob port. The
// composition root builds one from the blob.gcs configuration for the hot or
// the cold tier, or for a gcs member of a blob mirror. Tests outside the
// package build it on fake-gcs-server: the mirror's cross-cloud scenario and
// the Cloud CDN origin of the CDN acceptance feature. It imports
// platform/blob, platform/blob/session, platform/cdn, the standard library
// and the Cloud Storage client for Go.
//
// # Inputs
//
//   - ClientConfig, which mirrors the blob.gcs section of the configuration:
//     an endpoint for emulators, a service-account key file (otherwise
//     Application Default Credentials, which cover GKE Workload Identity), a
//     service account and private key that sign URLs locally (the account
//     alone signs through IAM signBlob), and WithoutAuthentication for
//     emulators. NewAPI builds the API over the official client, with JSON
//     reads, for a bucket that must exist. CanSign and SignsOverNetwork read
//     the configuration and the credential files, without a request, to tell
//     whether and how the client signs.
//   - Options for New: the object name prefix; the part size,
//     DefaultPartSize when zero, a multiple of 256 KiB and at least 1 MiB; the
//     session.Store that keeps sessions between requests (the composition
//     root passes session.InUploads under "gcs:<tier>", such as
//     "gcs:cold_blob", or "gcs:<tier>.<member>" for a mirror member);
//     Presign and SignsOverNetwork, which the composition root sets from
//     CanSign and SignsOverNetwork; and a cdn.Signer, such as a cdn.CloudCDN,
//     with which the store signs CDN URLs.
//   - What platform/blob's Store and Upload take: upload options, the bytes
//     appended, declared digests, upload IDs, the SHA-256 of a stored blob
//     and, to sign a URL, its blob.SignOptions.
//
// # Outputs
//
//   - Store, from New: a blob.Store that is also a blob.Signer, of V4
//     storage URLs when Presign is set and of CDN URLs when it has a CDN
//     signer, and a blob.PartSizer. ObjectKey gives the object name a CDN
//     signer addresses.
//   - API, the bucket operations the store calls, with Attrs, Source and
//     SignedURLOptions. NewAPI implements it over the SDK, and tests
//     substitute a fake.
//   - ClientConfig with CanSign and SignsOverNetwork, DefaultPartSize,
//     ErrPrecondition (a must-not-exist write or compose found its name
//     taken), ErrConfig, which every configuration error wraps, and ErrSeek.
//
// # Invariants
//
//   - A blob is stored under its lowercase hex SHA-256, and an object
//     reaches that name only after Commit has verified the digest. Parts and
//     folds are named p-<n> and c-<n> from one sequence, under the session's
//     prefix, and Walk lists only <prefix>sha256/.
//   - A part or fold is recorded only once its write succeeded, and one
//     whose name is taken adopts the object there only when its size and
//     CRC32C match, so a part whose response was lost is retried safely and
//     other bytes are never adopted. Compose sources are pinned by
//     generation, so a source replaced since it was recorded fails the
//     compose.
//   - A composed object whose size or CRC32C does not match the session is
//     deleted at the generation that compose created, and the commit fails:
//     no blob stays under a content address it does not match.
//   - Parts are buffered through platform/blob/session, and the session is
//     saved after every append. Its document holds the fold and the parts
//     with their names, generations, sizes and CRC32C, the next sequence
//     number and the CRC32C of everything flushed; the json tags of parts and
//     object are the storage format, because a session saved by one process
//     is resumed by another.
//   - After a commit or an abort, every object left under the session's
//     prefix is deleted by listing it, which also covers emulators that
//     ignore DeleteSourceObjects and parts written but never recorded.
//   - Objects are written as application/octet-stream with no content
//     encoding, so GCS never transcodes one and ranged reads see the stored
//     bytes. A reader opens the object only when read and again after a
//     Seek, so the seek-to-end probe of http.ServeContent costs no request.
//   - A storage URL lasts at most 7 days, the V4 limit. A store that signs
//     through IAM signBlob issues storage URLs on TTL-long windows and
//     caches them, one per blob, response overrides and window, each with at
//     least the TTL left, at most 4096 of them, the least recently used
//     dropped first (decision 0029); a TTL over half the V4 limit or under a
//     second is signed afresh. CDN URLs are signed locally and never cached.
//   - A store with a CDN signer answers blob.ViaCDN itself and is never
//     wrapped, so it keeps PartSizer (decision 0029); New refuses a signer
//     whose origin path does not contain the prefix.
//   - A missing object is blob.ErrNotFound. Errors the store creates start
//     "blob/gcs:", and those of platform/blob pass through unwrapped, because
//     their texts already name their package. Every text stays
//     byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Blobs": content addressing and upload sessions that resume across
//     requests).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     ("Shared session layer", and "gcs: compose of temporary part objects").
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md (a gcs
//     member of a mirror, whose chunks follow the member's part size).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Signing contract": V4 storage URLs, the cache of URLs signed over the
//     network, and Cloud CDN).
//   - Decision 0030: docs/research/decisions/0030-reference-deployments.md
//     (the gcp/single-bucket scenario that the real-cloud run uses, and its
//     conformance/ prefix).
//   - Research 18, section 1 (Google Cloud Storage) and section 4, "GCS
//     answers (2026-10-13)": docs/research/18-cloud-blob-storage.md.
//   - The cloud conformance workflow, .github/workflows/cloud-conformance.yml
//     (job gcs), and docs/testing/ci.md#real-cloud-conformance-manual.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 147 (platform/blob/gcs) and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2b-cloud-blob-storage-and-mirroring,
//     #p4b-reference-deployments-and-cdn-delivery and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/9
//   - https://github.com/weaveplatform/artifactweave/issues/983
//
// # Status
//
// Implemented P2b: the store, with parts composed onto the content key,
// folds before a 33rd source, the CRC32C check and presigned V4 GETs (issue
// 9, pull request 29), the real-cloud conformance run (issue 12) and
// PartSize, by which the mirror sizes its chunks (issue 39). P4b replaced
// the presigned GET with blob.Signer, whose storage URLs carry the file name
// and media type, and added CanSign (issue 119), the cache of URLs signed
// over the network with SignsOverNetwork (issue 147) and Cloud CDN URLs
// through a cdn.Signer (issue 121). Staged in P9d.
//
// # Testing
//
//   - TestAccContractOnFake and TestAccContractWithDurableSessions: the
//     blobtest contract on an in-memory fake of Cloud Storage, which models
//     what fake-gcs-server does not (preconditions on writes and compose
//     destinations, compose of at most 32 sources pinned by generation,
//     DeleteSourceObjects, the server's CRC32C check), the second with its
//     sessions kept in the uploads table of the platform's in-memory store.
//   - TestAccContractOnEmulator: the contract through the SDK adapter on
//     fake-gcs-server, which checks the wiring: requests, write
//     preconditions, compose, ranged JSON reads and listings. The emulator
//     validates no signature, so the sign case signs but does not fetch. It
//     needs a Docker engine that runs Linux containers and skips without one.
//   - TestUnitLayoutAndCleanup, TestUnitFoldBeyondThirtyTwoParts,
//     TestUnitDeclaredDigest, TestUnitPartWrittenTwiceIsAdopted and
//     TestUnitFoldAdoptsEarlierFold: the layout, with only the blob left
//     after a commit; folds over many resumed requests; a declared digest,
//     and a wrong one that leaves nothing behind; and a part or fold whose
//     write landed unseen, adopted, while other bytes under its name are not.
//   - TestUnitNewValidates, TestUnitAppendFailures, TestUnitCommitFailures,
//     TestUnitResumeWithDamagedState, TestUnitReadErrors,
//     TestUnitReaderCloseError and TestUnitDeleteWalkPresignErrors:
//     configuration refused with ErrConfig, and each API call failing through
//     the fake, among them a composed object that does not match, which is
//     deleted.
//   - TestUnitCDNSigning: CDN URLs for the prefixed object name, and a
//     signer that cannot reach the prefix refused.
//   - TestAccSignedURLCacheOverNetwork, TestAccSignedURLUncached,
//     TestAccSignedURLCacheEvicts and TestUnitURLCachePutReplaces: one
//     signBlob signature per blob, overrides and window, each URL with at
//     least the TTL left; local signers and long or sub-second TTLs signed
//     afresh; and the bounded cache, which drops the least recently used and
//     keeps one URL for a key put twice.
//   - TestUnitNormaliseEndpoint, TestUnitNewAPIValidates,
//     TestUnitSignedURLNeedsNoNetwork, TestUnitCanSign and TestUnitTranslate:
//     the SDK adapter's endpoint, configuration and local signing, how each
//     kind of credential signs, and the mapping onto the sentinels.
//   - TestAccContractOnRealGCS and TestAccRealGCSProbes: the contract on a
//     real bucket, with signed URLs fetched over HTTPS, which no emulator
//     validates, and the probes, which answer research 18's open questions as
//     PROBE lines. They skip unless ARTIFACTWEAVE_TEST_GCS_BUCKET is set. The
//     cloud conformance workflow dispatches them, and make conformance in the
//     gcp/single-bucket scenario runs them locally.
package gcs
