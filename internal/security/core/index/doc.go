// Package index will index artifacts, builds and releases for scanning.
//
// # Role
//
// security module, core layer.
//
// # Inputs
//
// None yet. Planned: the artifacts, builds and releases to index, reached
// through the owning modules' api packages and events (plan P26, tasks 2 and
// 3).
//
// # Outputs
//
// None yet. Planned: indexed components for graphs and analysis, with
// reindexing (plan P26, task 2 and exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 358.
//   - Plan:
//     docs/implementation/plan.md#p26-security-scanning-and-intelligence.
//   - Context map: the security row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P26.
package index
