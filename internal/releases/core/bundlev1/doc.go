// Package bundlev1 will hold release bundles v1: creation from AQL queries,
// the frozen artifact list, bundle states and storage in a releasebundles
// repository.
//
// # Role
//
// releases module, core layer.
//
// # Inputs
//
// None yet. Planned: bundle creation specs (AQL queries, path mappings, added
// properties and excluded property patterns), resolved once through the AQL
// engine (decision 0023).
//
// # Outputs
//
// None yet. Planned: bundle versions with their states and retention, stored
// through artifacts/api.Items in a releasebundles repository so that a bundle
// survives the deletion of its sources (decision 0023; plan P15, task 1).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 351.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution.
//   - Context map: the releases row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P15.
package bundlev1
