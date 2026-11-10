// Package archive will run the archive policies that move eligible items and
// package versions to the cold tier.
//
// # Role
//
// retention module, core layer. Archived-item records and the cold indexes
// belong to artifacts, and this module reaches them through accessors
// (decision 0044, persistence).
//
// # Inputs
//
// None yet. Planned: cold-storage archive policies and smart archiving
// policies, two policy families that one archiving engine and scheduler run
// (decision 0024).
//
// # Outputs
//
// None yet. Planned: archive runs that copy each blob to the cold store,
// record the archived item and remove it from the hot index, with preview,
// convertToAql and the execution status JFrog documents (decision 0024).
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
package archive
