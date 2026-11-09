// Package mirror is a blob store that keeps each blob on several member
// stores at once (decision 0028): different clouds, regions or accounts, any
// mix of the fs, s3, gcs and azure backends.
//
// Writes go to Redundancy members in parallel and succeed once WriteQuorum of
// them hold the blob; members still committing get MemberTimeout to finish
// in the background, and every member's outcome is recorded in the replica
// index (ReplicaIndex) as present or pending, so the repair worker can fill
// missing copies later. Reads use the index to go straight to a member
// holding the blob, in read order: this instance's zone first, then by
// observed latency, members whose breaker is open last. A read that fails
// part-way reopens on the next holder at the same offset, which is safe
// because blobs are immutable.
//
// This is Artifactory's sharding provider (redundancy, lenientLimit,
// zone-aware reads) with three differences: members may be any backend in
// any cloud, an index records which members hold which blob instead of every
// read probing every shard, and surplus copies are never deleted
// automatically.
//
// # Role
//
// platform layer: a blob store over several member stores. Blob storage
// belongs to no module, so the mirror sits in the platform beside the
// backends it spreads blobs over, and is itself a blob.Store and a
// blob.Signer. The composition root builds one for each blob tier configured
// with members (blob and cold_blob), runs its repair loop, and gives the
// storage endpoints its status and optimize trigger. BlobIndex and
// ReplicaIndex, the parts of the metadata store's blob and replica indexes
// that the mirror calls, are declared here with the replica rows they carry,
// by the package that consumes them, so that it imports no store (decision
// 0044, "Narrowing"); the metadata store's indexes satisfy them, and the
// repair lease is taken through platform/work's LeaseStore. The package
// imports platform/blob, platform/blob/session, platform/work, the kernel
// and the standard library.
//
// # Inputs
//
//   - Options for New: the Members (each a name, zone, blob store, kind and
//     whether it takes new copies), Redundancy, WriteQuorum, this instance's
//     Zone, MemberTimeout, ChunkSize, the breaker settings and the clock;
//     Sessions, the session.Store that keeps the mirror's own upload
//     sessions; and Replicas, the ReplicaIndex. The repair worker also takes
//     Blobs, the BlobIndex, and Leases, a work.LeaseStore, with RepairLease,
//     the batch, attempt and backoff settings, and OnRepair, which receives
//     each pass.
//   - Through the blob.Store and blob.Signer methods: uploads, appended over
//     one or more requests, and the SHA-256 of a stored blob for Open, Stat,
//     Exists, Delete and SignURL.
//   - RunRepair's interval and trigger, which the optimize endpoint signals.
//
// # Outputs
//
//   - Store, from New: a blob.Store and blob.Signer over the members, with
//     Repair, RunRepair, Status and WaitIdle; Member, Options and the
//     defaults; the sentinels ErrConfig, ErrQuorum and ErrSeek.
//   - The ports: BlobIndex, the one Get the repair worker calls, and
//     ReplicaIndex, the eight replica methods the mirror calls, without Get.
//   - The replica rows: BlobReplica, ReplicaState with ReplicaPresent,
//     ReplicaPending, ReplicaDeleting and ReplicaFailed, ReplicaFilter and
//     ReplicaCount.
//   - RepairReport, what one repair pass did, and Status, with MemberStatus
//     and RemovedMember, for the storage summary.
//
// # Invariants
//
//   - An upload succeeds once WriteQuorum members hold the blob, and fails
//     with ErrQuorum when fewer do.
//   - Every member's outcome is recorded in the replica index, as present
//     or pending, so that the repair worker fills each missing copy.
//   - Reads go to a holder in read order, and a read that fails part-way
//     reopens on the next holder at the same offset.
//   - Surplus copies are never deleted automatically: repair only adds
//     copies and removes those of deleted blobs, and it keeps a blob that
//     was uploaded again after its delete.
//   - A repair pass holds the lease that RepairLease names, so two mirrors
//     sharing a lease store, such as the hot and cold tiers, need different
//     names, or each pass of one skips the other.
//
// # References
//
//   - Decision 0028: docs/research/decisions/0028-blob-mirroring.md (quorum
//     writes, the replica index, zone-aware reads, background repair under
//     a lease, and no automatic deletion of surplus copies).
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the blob index, whose reference counts decide deferred deletes).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Narrowing", and "Type-alias bridges": the metadata store and the
//     shared types keep aliases of the replica rows while their users move).
//   - Research 18: docs/research/18-cloud-blob-storage.md, section 3
//     (Artifactory's sharding).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 148 (platform/blob/mirror), line 595 (the mirror's files) and
//     line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2b-cloud-blob-storage-and-mirroring
//     and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/22
//   - https://github.com/weaveplatform/artifactweave/issues/986
//
// # Status
//
// Implemented P2b: the replica rows (issue 15), the mirror with quorum
// writes and failover reads (issue 16, pull request 33), the repair worker
// with backfill and safe deferred deletes (issue 17, pull request 35), the
// cross-cloud scenario (issue 19, pull request 38) and chunks that align
// with every member's part size (issue 39, pull request 41); once P3 served
// the storage endpoints, Status and the optimize trigger (issue 18, pull
// request 66) and the replica records of removed members (issue 71, pull
// request 87). P4b: SignURL and Signs for download and CDN redirects, with
// signing failures kept out of member health (issue 119, pull requests 139
// and 146). P7: SHA-512 among the digests that uploads record and repair
// checks (issue 296, pull request 298). Staged in P9d: the index ports, with
// the replica rows beside them, and leases from platform/work.
//
// # Testing
//
//   - mirror_test.go: the blobtest contract on three fakes, on file systems
//     with a fake, with a redundancy of two of three and with durable
//     sessions; a member down at append or failing at commit, a missed
//     quorum, stragglers and the deletes and WaitIdle that wait for them, a
//     cancelled commit; read failover, also after an early EOF, the breaker
//     and a delete with a member down; signing; New's validation, chunk
//     sizes and their alignment with part sizes, placement, resumes that
//     drop lost members, and the failures of abort, walk, reads, the
//     replica index and session saves.
//   - repair_test.go: copies, stragglers found, a missing source, a backoff
//     that doubles and caps, copy failures, deferred deletes that never
//     remove a blob uploaded again, backfill to an added member, draining a
//     member that takes no new copies, skipping deleted and uncopied blobs,
//     the lease and its name, RunRepair, errors, batches, and a pass that
//     stops when its lease is taken over.
//   - status_test.go: TestUnitStatus and TestUnitStatusErrors.
//   - crosscloud_test.go: TestAccCrossCloud runs the contract and member
//     outages over S3 on MinIO, GCS on fake-gcs-server and Azure on
//     Azurite. It needs Docker and skips without it; CI runs it in the Linux
//     unit job.
//
// The tests are external (package mirror_test), and export_test.go opens
// the internals they reach. They build mirrors over testDB
// (testdb_test.go): the platform's in-memory upload and lease stores, and a
// blob and replica index that answers as the artifacts module's in-memory
// indexes do, since platform code, tests included, never imports a module.
// The artifacts contract runs the real indexes on the in-memory, SQLite and
// PostgreSQL stores, and the tests beside the metadata store's and the
// shared types' bridges fail if an alias of the replica rows becomes a
// declared copy.
package mirror
