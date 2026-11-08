// Package gc reclaims storage (decision 0010). A pass purges trash entries
// past their retention, reaps abandoned upload sessions, deletes blobs no
// item or trash entry references, and deletes content in the blob store that
// the index never recorded (a deploy whose transaction failed after its
// content was written).
//
// # Role
//
// artifacts module, core layer: the garbage collector. Passes run on a
// schedule and on POST /api/system/storage/gc, under a lease so two
// instances sharing a database never collect at once. A pass also removes
// the records of expired access tokens, through access/api's Tokens, the
// only way it reaches the access module (decision 0044, dependency rule 6).
// As artifacts core, it never imports the module's adapters or an ecosystem
// http package (dependency rule 8).
//
// # Inputs
//
//   - Driving: the composition root, which builds the Collector (New) with
//     the reapers of the multipart and registry upload sessions and the
//     registry's expiry of finished uploads, and runs a pass every interval
//     (Loop); and the storage API, whose POST /api/system/storage/gc runs
//     one pass (Run).
//   - Driven: the metadata store (leases, upload sessions, the blob index,
//     through the whole store), the blob store (platform/blob.Store) whose
//     content it deletes, the trash service (artifacts/core/trash), which
//     purges expired entries, the reapers and expirers Options names, and
//     accessapi.Tokens.
//
// # Outputs
//
//   - Report: what one pass did, or that it was skipped because another
//     instance held the lease.
//   - DefaultInterval (Artifactory's schedule, every four hours),
//     DefaultGrace and DefaultUploadExpiry: what a zero interval or a zero
//     Options field means.
//
// # Invariants
//
//   - Every blob is removed in three steps: its index row is marked
//     deleting, its content is deleted, and the row becomes a tombstone. A
//     deleting row refuses references, and a deploy that revives a tombstone
//     checks the content is still there, so a deploy of the same content
//     racing the removal is refused and repeated rather than left
//     referencing missing content.
//   - A pass holds the lease named "storage-gc" and renews it between steps,
//     so a crashed pass frees it within the lease's TTL. Every instance on
//     one database takes that name, so it never changes.
//   - Content is deleted only once it has been unreferenced, or unindexed,
//     for the grace period: content is written before its reference, so
//     younger content may be a deploy about to commit.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the blob index and content-addressed content).
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (how long a token record outlives its token).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 204 (core/gc), line 1027 (the mapping row) and rule 1 at lines 30
//     to 34.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (trash can and garbage collection, #48) and
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
//   - gc_test.go and gc_more_test.go: passes over a store and a blob store,
//     the grace period, reapers and expirers, orphaned content, the lease
//     held elsewhere, failing steps and the loop.
//   - race_test.go: no content is lost to a deploy racing a removal, and a
//     deploy finishes a crashed pass's removal.
//   - tokens_test.go: expired token records removed through
//     accessapi.Tokens.
package gc
