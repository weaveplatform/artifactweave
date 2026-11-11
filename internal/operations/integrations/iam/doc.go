// Package iam will map AWS IAM identities.
//
// # Role
//
// operations module, adapter layer for an external integration. The module is
// flat, so its integrations sit under integrations/ beside its own files.
//
// # Inputs
//
// None yet. Planned: AWS IAM mapping settings (plan P38, task 1).
//
// # Outputs
//
// None yet. Planned: mapped identities whose effect is proved against a
// controlled real service (plan P38, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 361.
//   - Plan:
//     docs/implementation/plan.md#p38-operations-and-external-integrations.
//   - Context map: the operations row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P38.
package iam
