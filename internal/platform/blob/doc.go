// Package blob is the content-addressed binary store behind every repository
// (decision record 0004). Blobs are keyed by their lowercase hex SHA-256; the
// SHA-512, SHA-1 and MD5 that package clients also use are computed in the
// same pass during the upload and returned by Commit so the caller can record
// them in the item index, which is where checksum deploy, checksum search and
// checksum files read them.
//
// Uploads are sessions rather than single calls because two clients need to
// append across HTTP requests: Docker chunked uploads (PATCH then PUT) and the
// JFrog CLI's multipart upload. A session is identified by an opaque ID that
// ResumeUpload accepts in a later request. One writer per session is assumed;
// the HTTP layer serialises requests for the same upload.
//
// Backends: blob/fs (local disk), blob/s3 (S3-compatible object storage),
// blob/gcs (Google Cloud Storage) and blob/azure (Azure Blob Storage), plus
// blob/mirror, one store over several of them. All are held to the same
// behaviour by blob/blobtest.
//
// # Role
//
// platform layer: the content-addressed blob port. No module owns blob
// storage, so the port and the hashing and digest checks that every backend
// shares sit in the platform, with the backends below it. In the artifacts
// module the item, registry, multipart and build services, the format
// handlers and the HTTP servers write and serve content through Store, and
// garbage collection walks and deletes through it; the composition root
// opens a store for each configured tier. Blob, the row of the metadata
// store's blob index, is declared here as well, so that the mirror's repair
// worker, which reads it through its own port, imports no store. It imports
// the kernel and the standard library.
//
// # Inputs
//
//   - The bytes of an upload, appended to a session over one or more
//     requests, and the digests a client declares: before the body through
//     WithExpectedSHA256 (Docker's monolithic PUT ?digest=,
//     X-Checksum-Sha256), and at Commit as want.
//   - Upload IDs for ResumeUpload, and the SHA-256 that names a stored blob
//     for Open, Stat, Exists, Delete and SignURL.
//   - For a Signer, the route (Via) and SignOptions: how long the URL stays
//     valid, and the file name and media type it carries as response
//     overrides.
//
// # Outputs
//
//   - Store and Upload, the contract every backend implements: begin,
//     resume, append, commit and abort an upload; open a blob for ranged
//     reads, stat, check, delete and walk. Info and Digests describe a blob.
//   - Signer, Via (ViaStorage, ViaCDN), SignOptions and Disposition: signed
//     download URLs for repositories that set downloadRedirect or
//     cdnRedirect, naming the file as a streamed download does.
//   - PartSizer, which backends that write fixed-size parts implement, so
//     the mirror can size its chunks to every member's parts.
//   - UploadOption, UploadOptions, WithExpectedSHA256 and ResolveOptions,
//     for BeginUpload.
//   - Hasher (NewHasher, Write, Sum, MarshalBinary, UnmarshalBinary),
//     CopyHashed and Verify: the one-pass hashing and the digest check that
//     every backend shares.
//   - The validators ValidateSHA256, ValidateDigests and ValidUploadID, and
//     NewUploadID.
//   - The sentinels ErrNotFound, ErrDigestMismatch, ErrUploadUnknown,
//     ErrInvalidDigest, ErrCannotSign and ErrHasherState. Backends wrap
//     them, and callers test them with errors.Is.
//   - Blob, the blob index row: a blob's checksums, size, creation time,
//     reference count, and its State in garbage collection with the time
//     it was marked or removed; BlobState with BlobLive, BlobDeleting and
//     BlobDeleted. The metadata store keeps the rows, and the mirror's
//     BlobIndex returns them.
//
// # Invariants
//
//   - A blob is keyed by its lowercase hex SHA-256, and ValidateSHA256
//     accepts nothing else. Backends build file and object names from that
//     digest and from upload IDs, so the validators are also the guard
//     against path traversal.
//   - SHA-512, SHA-1 and MD5 come from the same pass as the SHA-256, and
//     Commit returns all four. CopyHashed feeds the Hasher only the bytes a
//     backend stored, so after a short or failed write the session still
//     matches its content and the client continues from Size.
//   - One writer per upload session: the HTTP layer serialises a session's
//     requests. A session accepts appends from whoever presents its ID, so
//     NewUploadID draws IDs from crypto/rand.
//   - Committing content that is already stored succeeds and discards the
//     session, so the same bytes committed twice, or from two sessions at
//     once, end as one blob.
//   - The hasher state is persisted: fs keeps it in each session's state
//     file and the object-store backends in their saved sessions, and
//     UnmarshalBinary reads it back in a later request or process. Its
//     encoding (the SHA-256, SHA-512, SHA-1 and MD5 states, in that order,
//     each behind a two-byte big-endian length) must not change, and
//     TestUnitHasherStateKnownAnswer pins it. A change still compiles, and
//     a round trip within one build cannot see it. At run time the sessions
//     saved before it would no longer restore: fs rehashes their data and
//     carries on, while the object-store backends report ErrUploadUnknown
//     and the client starts the upload again.
//   - Error texts reach clients, for example in the JFrog CLI's multipart
//     upload status, so they stay byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Blobs": content addressing, the checksums of one pass and the
//     upload sessions).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the blob row's states while garbage collection deletes a blob).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     (the Google Cloud Storage and Azure backends behind this contract).
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md (one
//     store over several; its chunks follow every member's part size).
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Signing contract": Signer, Via and SignOptions).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md (the
//     cold tier, on the same backends).
//   - Research 18 and 19: docs/research/18-cloud-blob-storage.md and
//     docs/research/19-cdn-and-download-redirect.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 146 to 151 (platform/blob and its subpackages) and line 1049
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 3,
//     #p2b-cloud-blob-storage-and-mirroring,
//     #p4b-reference-deployments-and-cdn-delivery, #p7-npm-go-maven-locals
//     (prework 1) and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/980
//   - https://github.com/weaveplatform/artifactweave/issues/986
//
// # Status
//
// Implemented P2: the store contract, upload sessions with the saved hasher
// state, the validators and the sentinels, and Blob, the blob index row. P2b
// added PartSizer, by which the mirror sizes its chunks (issue 39). P3 gave
// the blob row its garbage-collection states (issue 64). P4b replaced the
// presigner with the signing capability of download and CDN redirects:
// Signer, Via, SignOptions, Disposition and ErrCannotSign (issue 119). P7
// made SHA-512 a digest of every upload (issue 296). Staged in P9d.
//
// # Testing
//
//   - TestUnitHasherSaveRestoreContinues: a state saved part way and
//     restored gives the digests of one pass over the whole stream.
//   - TestUnitHasherStateKnownAnswer: the state saved after a fixed input
//     has the framing above and a fixed SHA-256, and once restored it
//     continues to the digests shasum and md5 give for the whole input.
//   - TestUnitHasherRejectsBadState: an empty, truncated, overlong or
//     corrupt state fails with ErrHasherState and leaves the hasher as it
//     was.
//   - TestUnitVerify, TestUnitValidate and TestUnitResolveOptions: the digest
//     check, the validators and the options, with their sentinels; an upload
//     ID with path characters is invalid.
//   - TestUnitCopyHashedKeepsHashInStepWithWrites: after a short write, a
//     write error or a read error, the hash covers exactly the stored bytes.
//   - TestUnitNewUploadID: an ID has the shape ValidUploadID accepts, and a
//     failing random source is an error.
//
// Every backend runs the blobtest contract: fs, s3, gcs, azure and mirror.
// The metadata store's blob index contract runs on its in-memory, SQLite and
// PostgreSQL stores.
package blob
