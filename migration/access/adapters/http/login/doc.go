// Package login will serve browser login and the native-client approval
// endpoints.
//
// # Role
//
// access module, adapter layer (HTTP), below access/adapters/http.
//
// # Inputs
//
// None yet. Planned: browser login requests and the approval requests of
// native clients, such as npm web login and Terraform login.v1 (plan P21,
// task 1).
//
// # Outputs
//
// None yet. Planned (target layout, line 295): responses over
// access/core/session: sessions, consent, approvals and the scoped
// credentials that an approved client receives.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 295.
//   - Plan:
//     docs/implementation/plan.md#p21-browser-and-enterprise-authentication.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - https://github.com/weaveplatform/artifactweave/issues/262
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P21.
package login
