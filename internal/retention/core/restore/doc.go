// Package restore will run the restore jobs that bring archived items back
// from the cold tier.
//
// # Role
//
// retention module, core layer.
//
// # Inputs
//
// None yet. Planned: restore jobs over archived items (plan P16, task 1).
//
// # Outputs
//
// None yet. Planned: restored items, byte-identical to what was archived, and
// the jobs' executions (plan P16, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 328.
//   - Plan:
//     docs/implementation/plan.md#p16-cold-storage-archive-and-retention.
//   - Context map: the retention row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P16.
package restore
