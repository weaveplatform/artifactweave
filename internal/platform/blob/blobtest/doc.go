// Package blobtest is the contract every blob.Store backend must satisfy.
// Backends call Run from their own tests; each object-store backend runs it
// against an in-memory fake everywhere and against its emulator where Docker
// is available.
//
// # Role
//
// platform layer, test support for platform/blob: the contract every
// backend must pass. Only tests import it, and the test files of any module
// may (decision 0044, dependency rule 1). It imports platform/blob and the
// standard library.
//
// # Inputs
//
//   - newStore, which must return an empty store each time it is called.
//     Run calls it once per case, so no case sees another's blobs.
//   - Options. LargeSize is the size of the blob that exercises multi-part
//     paths; a backend that buffers into parts sets it above its part size,
//     and zero means 3 MiB. FetchPresigned makes the sign case GET the
//     signed URL over HTTP and check the body and the response overrides,
//     which only a real object store answers.
//
// # Outputs
//
//   - Run: the contract, one subtest per case in name order: abort,
//     cancelled context, concurrent same bytes, delete and not found, digest
//     mismatch, empty blob, expected sha256, idempotent commit, invalid
//     digests, large blob, ranged reads, resume, round trip, sign and walk.
//     The sign case skips a store that does not sign storage URLs.
//   - Digest, the four digests of a byte slice and the oracle the cases
//     check against; Content, deterministic bytes for a seed; and Put, one
//     append and a commit. Other tests seed stores with them.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     ("Contract suites").
//   - .testcoverage.yml excludes ^internal/platform/blob/blobtest/ as a
//     contract-suite harness (decision 0015): its remaining branches are the
//     t.Fatal paths that only a backend breaking the contract takes.
//   - Decision 0004 ("Verification"), decision 0027, decision 0028 and
//     decision 0029 ("Signing contract"), whose backends, mirror and signed
//     URLs the cases hold to one behaviour.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rule 1).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 151 (platform/blob/blobtest), line 848 (the port contract suites)
//     and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 5 and the exit
//     criteria, #p2b-cloud-blob-storage-and-mirroring (exit criteria) and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/980
//
// # Status
//
// Implemented P2. P4b replaced the presign case with the sign case, which
// checks Signs, the refusal of a CDN route the store lacks (ErrCannotSign)
// and the response overrides (issue 119). P7 added SHA-512 to the oracle
// and to the digest cases (issue 296). Staged in P9d.
//
// # Testing
//
// The package has no tests of its own. Every backend runs Run: fs on a
// temporary directory; s3, gcs and azure on an in-memory fake, again with
// their sessions kept in the uploads table, on MinIO, the GCS emulator and
// Azurite where Docker runs Linux containers, and on the real clouds in the
// opt-in cloud conformance workflow; and mirror over fakes, over
// filesystems and a fake, with two of three members, with durable sessions
// and across clouds.
package blobtest
