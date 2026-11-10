// Package model holds the releases module's entity values: release bundles
// v1 with their specs, frozen artifact lists, retention marks and exports,
// and their distribution to edges through trackers, rules and per-target
// jobs.
//
// # Role
//
// releases module, core layer, entity values. The module's store ports take
// and return them, and P15's bundle and distribution services will build on
// them. The package imports the standard library and the kernel (the
// checksums of a bundle artifact). SigningKey, the key bundles are signed
// with, is not here: it is one of the access module's key records, which
// STO-10 moves.
//
// # Inputs
//
//   - For SelectTargets, distribution rules and the targets they select
//     from; for TrackerStatus, a tracker's jobs.
//
// # Outputs
//
//   - ReleaseBundle with BundleState (with Editable) and BundleOrigin;
//     BundleSpec with BundleQuery, PathMapping and PropertyValues;
//     ReleaseNotes; BundleArtifact; BundleExport.
//   - BundleRetentionConfig, BundleRetentionAudit and BundleOperation.
//   - DistributionTracker with TrackerKind, DistributionJob, JobStatus with
//     TrackerStatus, and DistributionTarget and DistributionRule with
//     Matches and SelectTargets.
//
// # Invariants
//
//   - Signing freezes a bundle version: only an OPEN bundle is Editable.
//   - Job statuses are Distribution's display strings, as jfrog-client-go
//     and the archived REST examples show them (research 17), except
//     Aborted, artifactweave's own, which no published example shows.
//   - A rule's empty fields match every target, and its site name takes '*'
//     wildcards, as JFrog's rules do.
//
// # References
//
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence").
//   - Research 16: docs/research/16-enterprise-products.md. Research 17:
//     docs/research/17-undocumented-shapes.md (bundle states and job
//     statuses). Research 15: docs/research/15-data-model.md, lines 709 to
//     714 (the bundle and distribution tables) and line 760 (bundle
//     retention as built).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton ahead of its phase), lines 348 to
//     353 (the releases module), lines 577 and 578 (each module owns its
//     store ports) and line 1058 (the shared types split into each module's
//     core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0,
//     #p15-release-bundles-v1-and-distribution and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: the release bundle and distribution values among the
// shared domain types (pull request 3). Staged in P9d. The behaviour that
// uses them arrives with P15.
//
// # Testing
//
//   - distribution_test.go: TestUnitDistribution (Editable, rule matching
//     and target selection, and tracker status from its jobs).
//
// The metadata store's Bundles, BundleRetention, BundleExports and
// Distribution contract subtests store and read these values on its
// in-memory, SQLite and PostgreSQL stores.
package model
