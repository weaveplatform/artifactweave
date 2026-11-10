// Package session is the upload session shared by the object-store blob
// backends (s3, gcs, azure; decision 0027). Object stores cannot append, so
// each backend buffers appended bytes into parts and uploads a part whenever
// one is full; between HTTP requests the session must survive in the
// metadata store so a Docker chunked upload or a CLI multipart upload
// resumes in a later request or another process.
//
// A Session holds what every backend needs: the size, the declared digest,
// the saved blob.Hasher state and the pending tail shorter than one part,
// plus a backend-specific document D (S3's multipart upload ID and part
// list, GCS's part objects, Azure's block count). It is persisted through a
// Store after every append. InUploads keeps sessions in the uploads table
// under a per-store backend name such as "s3:hot", so two stores of one kind
// never share or misattribute sessions.
//
// # Role
//
// platform layer: the upload session of the object-store blob backends, and
// the port of the uploads table it is persisted through. Blob storage belongs
// to no module, so the sessions sit in the platform beside platform/blob,
// whose hasher, upload options and sentinels they use. The s3, gcs and azure
// stores keep their sessions through it, and so does the mirror for the
// uploads it spreads over its members. Upload and UploadStore are declared
// here, by the package that persists through them, so that it imports no
// store (decision 0044, "Narrowing"). The platform's store adapters
// (platform/store/sql and platform/store/inmem) implement the port over the
// uploads table. Through the metadata store, the Docker registry and the
// JFrog CLI multipart service keep their own sessions in the same table, and
// the stale-upload reaper of garbage collection lists the idle ones with
// Stale. The package imports platform/blob, the kernel and the standard
// library.
//
// # Inputs
//
//   - From a backend: the upload options for Begin (a declared SHA-256), an
//     upload ID for Resume, the appended bytes as an io.Reader with the
//     backend's part size and its FlushFunc, and the digests to Verify.
//   - A Store to persist through: InUploads over an UploadStore, the
//     metadata store's uploads, and a backend name for the store instance
//     (the composition root uses "<backend>:<tier>", such as "s3:blob" or
//     "gcs:cold_blob", "<backend>:<tier>.<member>" for a mirror member and
//     "mirror:<tier>" for the mirror); or NewMemory, which keeps sessions in
//     process memory for tests and embedders without a metadata store.
//
// # Outputs
//
//   - Session[D], from Begin or Resume, with Append, Flush, Save, Live,
//     Verify and Forget; FlushFunc, which hands one part to the backend.
//   - Store, what a session is persisted through (Put, Get, Delete), with
//     Memory (NewMemory) and InUploads.
//   - Upload, one row of the uploads table, and UploadStore, the port the
//     platform's stores implement: Put, Get, Delete, and Stale for the
//     reaper.
//
// # Invariants
//
//   - A session is saved after every append, also when the flush or the
//     read fails, so Size counts every byte read and hashed and the client
//     can continue from it. A part whose flush fails stays pending, and the
//     next Append or Commit retries it.
//   - A session stored under another backend name is unknown to InUploads,
//     so the stale-upload reaper always knows which store aborts it: each
//     row names its store, and a store resumes only its own.
//   - Get of an unknown ID wraps blob.ErrUploadUnknown, and Delete of a
//     missing session succeeds, in Memory and InUploads alike. Resume of an
//     unknown, malformed or damaged session also wraps blob.ErrUploadUnknown:
//     there is no local copy of the uploaded parts to rehash, so the session
//     ends.
//   - InUploads keeps a session's Started on every save and sets Updated to
//     the time of the save, which is what Stale compares.
//   - The stored form is JSON, the record's fields and each backend's
//     document under its json tags. A session saved by one process is
//     resumed by another, so the encoding stays as it is.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata" and "Blobs": the metadata store, and upload sessions that
//     resume across requests).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     ("Shared session layer": object stores cannot append, and sessions live
//     in the uploads table under a per-instance backend name).
//   - Research 15, section 4 (the uploads table):
//     docs/research/15-data-model.md; research 18:
//     docs/research/18-cloud-blob-storage.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Narrowing").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 149 (platform/blob/session) and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p2b-cloud-blob-storage-and-mirroring and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/8
//   - https://github.com/weaveplatform/artifactweave/issues/982
//
// # Status
//
// Implemented P2: Upload and UploadStore, with Stale for the stale-upload
// reaper that garbage collection runs from P3 (pull request 3). P2b: the
// shared session layer, with Session, Store, Memory and InUploads under
// per-store backend names (issue 8, pull request 30). Staged in P9d.
//
// # Testing
//
//   - TestUnitAppendFlushesFullPartsAndResumes: full parts go to the flush
//     and the tail stays pending; a resumed session continues where it
//     stopped, Verify gives the digests of everything appended, and after
//     Forget, Live and Append report blob.ErrUploadUnknown.
//   - TestUnitVerify: the declared SHA-256 with a matching MD5 passes, and a
//     declared digest of other content is blob.ErrDigestMismatch.
//   - TestUnitAppendFailures: a failed flush keeps the bytes pending and
//     counted; a failed read, save or forget, data that cannot be encoded, a
//     cancelled context and an invalid declared digest each fail with their
//     cause.
//   - TestUnitResumeRejects: a malformed record, hasher state or document, an
//     invalid ID and an ID never issued are blob.ErrUploadUnknown.
//   - TestUnitMemoryCopies: Memory copies on Put and Get, and a deleted
//     session is unknown.
//   - TestUnitInUploadsSeparatesStores: decision 0027's check that two stores
//     of one kind share the uploads table without seeing each other's
//     sessions, and that each row names its store.
//   - TestUnitInUploadsRoundTripKeepsStart: a save keeps Started and moves
//     Updated, and forgetting twice succeeds.
//   - TestUnitInUploadsErrors: the upload store's errors are wrapped and not
//     taken for an unknown session, and a missing row lets Put start one.
//
// The InUploads tests keep their sessions in the platform's in-memory store.
// The platform's upload contract (the Uploads subtest of
// platform/store/storetest) runs on the platform's in-memory and SQLite
// stores and on the metadata store's in-memory, SQLite and PostgreSQL stores,
// and the s3, gcs, azure and mirror stores run the blob contract with
// sessions kept through InUploads.
package session
