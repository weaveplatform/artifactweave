// Package scim will serve the SCIM endpoints: discovery, users, groups,
// filters, pagination and PATCH.
//
// # Role
//
// access module, adapter layer (HTTP), below access/adapters/http.
//
// # Inputs
//
// None yet. Planned: SCIM requests from provisioning clients (plan P22, task
// 1).
//
// # Outputs
//
// None yet. Planned (target layout, line 294): SCIM responses over
// access/core/scim.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 294.
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
