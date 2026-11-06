// Package engine will connect the analysis engines that the security module
// runs.
//
// # Role
//
// security module, adapter layer.
//
// # Inputs
//
// None yet. Planned: the explicit analysis engines that plan P26 selects
// (task 1).
//
// # Outputs
//
// None yet. Planned: analysis results for security/core/analysis; an engine
// that is missing or fails is reported as such, never as a clean result (plan
// P26, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 359.
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
package engine
