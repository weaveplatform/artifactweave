// Package application will hold applications: versions, package binding,
// stages and lifecycle transitions, ownership and release eligibility.
//
// # Role
//
// governance module, core layer.
//
// # Inputs
//
// None yet. Planned: application definitions and versions with their bound
// packages, under the project roles of P11 (plan P25, task 1).
//
// # Outputs
//
// None yet. Planned: application versions that advance through stages only
// when authorised and eligible (plan P25, task 1 and exit criteria).
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
package application
