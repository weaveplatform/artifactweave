// Package scim will hold SCIM provisioning: user and group reconciliation,
// conflict rules and deprovisioning.
//
// # Role
//
// access module, core layer. Its endpoints are access/adapters/http/scim.
//
// # Inputs
//
// None yet. Planned: SCIM requests for users and groups, with filters and
// PATCH operations, decoded by access/adapters/http/scim.
//
// # Outputs
//
// None yet. Planned (target layout, line 550): users.go, groups.go,
// filter.go, patch.go and reconcile.go: idempotent reconciliation of
// provisioned identities, and deprovisioning that reaches effective
// permissions, sessions and tokens (plan P22, task 2).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 288, and "Access and identity", line 550.
//   - Plan: docs/implementation/plan.md#p22-scim-and-identity-lifecycle.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P22.
package scim
