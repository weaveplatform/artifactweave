// Package policy will hold Unified Policy: templates, rules, policies and
// evaluation records.
//
// # Role
//
// governance module, core layer.
//
// # Inputs
//
// None yet. Planned: templates, rules and policies, and the evidence, release
// and security inputs they evaluate (plan P25, tasks 2 and 3).
//
// # Outputs
//
// None yet. Planned: evaluation records that are traceable and reproducible;
// missing required security evidence never yields an approval (plan P25, exit
// criteria).
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
package policy
