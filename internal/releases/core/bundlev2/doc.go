// Package bundlev2 will hold immutable release bundles v2, created from
// builds, artifacts, packages, AQL and existing bundles.
//
// # Role
//
// releases module, core layer.
//
// # Inputs
//
// None yet. Planned: release bundle v2 creation requests from builds,
// artifacts, packages, AQL queries and existing bundles (plan P24, task 1).
//
// # Outputs
//
// None yet. Planned: immutable, signed v2 releases with snapshots and
// collision handling, which deleting their sources cannot corrupt (plan P24,
// task 1 and exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 351.
//   - Plan: docs/implementation/plan.md#p24-lifecycle-and-release-bundles-v2.
//   - Context map: the releases row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P24.
package bundlev2
