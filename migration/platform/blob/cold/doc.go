// Package cold will wrap the cold blob tier for archiving and stage the
// content that restore jobs bring back.
//
// # Role
//
// platform layer: blob storage infrastructure with no business rules. The
// retention module decides what to archive and restore; this package handles
// the cold copies.
//
// # Inputs
//
// None yet. Planned: the cold_blob store that the application configuration
// defines beside the hot blob store, on the same backends (decision 0024).
// That store is configured and opened today as a second blob tier; P16 adds
// the archive wrapper and the restore staging.
//
// # Outputs
//
// None yet. Planned (target layout, line 150): the cold-tier wrapper that
// archive runs write through, and the staging area that restore jobs read
// from.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 150.
//   - Plan:
//     docs/implementation/plan.md#p16-cold-storage-archive-and-retention.
//   - Context map: platform has no row; the retention row owns the cold tier
//     and restore jobs
//     (https://github.com/weaveplatform/artifactweave/issues/958).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P16.
package cold
