// Package analysis will run vulnerability and license analysis.
//
// # Role
//
// security module, core layer.
//
// # Inputs
//
// None yet. Planned: component graphs, and the vulnerability and license
// intelligence of the selected engines and sources (plan P26, tasks 1 and 2).
//
// # Outputs
//
// None yet. Planned: findings with their provenance and freshness; a missing
// engine or source never yields a clean result (plan P26, exit criteria).
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
package analysis
