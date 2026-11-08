// Package multipart serves the JFrog CLI's multipart upload protocol, as the
// pinned jfrog-client-go drives it (artifactory/services/utils/
// multipartupload.go): create a session, get a URL per part, PUT the parts
// concurrently, complete with the file's SHA-1, poll the status, and abort on
// failure.
//
// # Role
//
// artifacts module, core layer: the multipart upload sessions. Once the
// status is FINISHED the CLI checksum-deploys the file, so a session only has
// to store the content and index it; the item, its properties and the
// package index come from that ordinary checksum deploy. Artifactory hands
// out presigned S3 URLs and the object store assembles the parts.
// artifactweave serves the part URLs itself and keeps one blob.Store upload
// session per multipart upload (decision 0004), so every backend, mirror
// included, takes multipart uploads. It checks API restrictions through
// access/api's Restrictions, the only way it reaches the access module
// (decision 0044, dependency rule 6), and as artifacts core it never imports
// the module's adapters or an ecosystem http package (dependency rule 8).
//
// # Inputs
//
//   - Driving: the multipart upload routes under
//     /artifactory/api/v1/uploads (create, urlPart, part, complete, status
//     and abort); the checksum deploy, which asks Finished whether a
//     checksum token lets it through; the garbage collector, which hands
//     stale sessions under Backend to Reap; and the composition root, which
//     runs Sweep at startup.
//   - Driven: the metadata store (the uploads table, and the blob index a
//     completion records the content in), the blob store
//     (platform/blob.Store) whose upload the parts are appended to, the
//     staging directory Dir on local disk, and accessapi.Restrictions.
//
// # Outputs
//
//   - Upload tokens, signed part URLs, the state a status poll reads
//     (Status, with the Status* values the CLI understands) and, once
//     finished, the checksum token the deploy presents.
//   - Errors: ErrBadToken, ErrInvalid, ErrState and ErrCorrupt, which the
//     HTTP layer maps onto statuses.
//
// # Invariants
//
//   - Backend ("jfrog-multipart") names the sessions' rows in the uploads
//     table, so it is stored and never changes.
//   - Parts are staged on local disk as they arrive, in any order, and
//     appended to the blob upload in order. Only the parts that arrive ahead
//     of a missing one wait on disk, a few times the part size with the
//     CLI's concurrency.
//   - The CLI sends parts without credentials, so a part URL carries a
//     signature for that upload and part only, and a session's user is
//     checked against API restrictions on every request that opens the
//     session by its token and on every part (decision 0043).
//   - Error texts reach the CLI, ErrInvalid's and ErrState's in a response
//     body and a failed completion's in Status, so they stay
//     byte-identical.
//
// # References
//
//   - Research 09: docs/research/09-jfrog-cli-compatibility.md, "Multipart
//     upload" (the protocol at the commit the pinned jf uses).
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (blob store upload sessions).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     (temporary restrictions).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 205 (core/multipart), line 1030 (the mapping row) and rule 1 at
//     lines 30 to 34.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (the JFrog CLI multipart protocol, #91) and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1012
//
// # Status
//
// Implemented P3 to P9c. Staged in P9d.
//
// # Testing
//
//   - multipart_test.go: round trips with parts out of order, tokens and
//     part signatures, API restrictions (from the access test kit,
//     access/accesstest), the checksum token, invalid requests, completion,
//     restarts, and abort, reap and sweep.
//   - fault_test.go: store, blob store and staging failures.
package multipart
