// Package controlpoint will hold evidence-based control points, which an
// application version must pass to advance.
//
// # Role
//
// governance module, core layer.
//
// # Inputs
//
// None yet. Planned: control point definitions and the evidence collected for
// an application version (plan P25, task 2).
//
// # Outputs
//
// None yet. Planned: pass or fail decisions, with explicit behaviour for
// stale or missing evidence and unavailable policy inputs (plan P25, task 3).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 357.
//   - Plan: docs/implementation/plan.md#p25-apptrust-and-policy-governance.
//   - Context map: the governance row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P25.
package controlpoint
