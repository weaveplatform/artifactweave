// Package gc reclaims storage (decision 0010). A pass purges trash entries
// past their retention, reaps abandoned upload sessions, deletes blobs no
// item or trash entry references, and deletes content in the blob store that
// the index never recorded (a deploy whose transaction failed after its
// content was written).
//
// # Role
//
// artifacts module, core layer: the garbage collector and the storage
// policy (Policies) that the descriptor's gcConfig and trashcanConfig set.
// Passes run on the shared Quartz scheduler at gcConfig.cronExp and on
// POST /api/system/storage/gc, under a lease so two instances sharing a
// database never collect at once. A pass also removes
// the records of expired access tokens, through access/api's Tokens, the
// only way it reaches the access module (decision 0044, dependency rule 6).
// As artifacts core, it never imports the module's adapters or an ecosystem
// http package (dependency rule 8).
//
// # Inputs
//
//   - Driving: the composition root, which builds the Collector (New) with
//     the reapers of the multipart and registry upload sessions and the
//     registry's expiry of finished uploads, and binds Scheduled to
//     ScheduleKind on the shared scheduler (platform/schedule); the storage
//     API, whose POST /api/system/storage/gc runs one pass (Run); and the
//     configuration descriptor, through artifacts/api's StoragePolicies,
//     which Policies implements.
//   - Driven: the metadata store, through Store (the lease table, upload
//     sessions, and the trash can's store, trash.Store, which holds the blob
//     index), the blob store (platform/blob.Store) whose
//     content it deletes, the trash service (artifacts/core/trash), which
//     purges expired entries, the reapers and expirers Options names, and
//     accessapi.Tokens. Policies reads and writes the trash can settings and
//     the collector's schedule row (PolicyStore).
//
// # Outputs
//
//   - Report: what one pass did, or that it was skipped because another
//     instance held the lease.
//   - DefaultGrace and DefaultUploadExpiry: what a zero Options field means.
//   - Policies: the effective policy (Current), with Artifactory's defaults
//     (DefaultCron, every four hours; the trash can on for 14 days); Plan
//     and Apply of a change, refused as *api.StoragePolicyInvalid; and
//     EnsureSchedule, which creates the default schedule on a start that
//     finds none.
//   - Scheduled: the handler of ScheduleKind, one pass per fire.
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
//   - The schedule row (ScheduleKind, "gcConfig") is the one copy of the
//     expression: a restart or a repeated apply finds it and adds none, and
//     an unchanged expression keeps its revision and position. A changed
//     one takes a fresh revision, so an occurrence queued under the old one
//     finishes without a pass. Policies writes in the transaction ctx
//     carries, so a descriptor that fails later rolls the trash can
//     settings and the schedule back with it.
//   - A fire whose successor is already due is skipped, so fires missed
//     while no node ran give one pass, as Quartz's default cron misfire
//     instruction does in Artifactory.
//   - A retention period is refused when negative or when it reaches back
//     before Artifactory's floor (February 2003); zero purges at the next
//     pass. A cron expression is refused when it does not parse or never
//     fires again, as Quartz refuses to schedule such a trigger.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1012,
//     https://github.com/weaveplatform/artifactweave/issues/1088 and
//     https://github.com/weaveplatform/artifactweave/issues/1829 (P12
//     CFG-05: gcConfig and trashcanConfig).
//   - https://docs.jfrog.com/administration/docs/regular-maintenance-operations.md
//     (every four hours by default) and
//     https://docs.jfrog.com/installation/docs/advanced-configurations-in-artifactory-yaml.md
//     (gcConfig.cronExp).
//
// # Status
//
// Implemented P3 to P9c. Moved here in P9d. P12 CFG-05 added Policies and
// moved scheduled passes from a fixed interval to the shared Quartz
// scheduler.
//
// # Testing
//
//   - gc_test.go and gc_more_test.go: passes over a store and a blob store,
//     the grace period, reapers and expirers, orphaned content, the lease
//     held elsewhere and failing steps.
//   - policy_test.go: the policy's defaults, idempotent apply, refusals,
//     store failures and the restart that keeps a schedule; Scheduled's
//     shutdown, skipped and failed fires; and TestAccStoragePolicyEffects,
//     on memory, SQLite and PostgreSQL, the scheduled passes' effects:
//     retention kept then expired, restore, shared bytes kept, a disabled
//     trash can, missed fires, a superseded occurrence and rollback.
//   - race_test.go: no content is lost to a deploy racing a removal, and a
//     deploy finishes a crashed pass's removal.
//   - tokens_test.go: expired token records removed through
//     accessapi.Tokens.
package gc
