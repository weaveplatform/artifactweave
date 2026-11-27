// Package session implements bounded browser approval, session-bound CSRF,
// native-client PKCE and callback validation.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// Browser password/consent, npm polling capabilities and Terraform S256
// challenges/verifiers with exact localhost callbacks. Identity, authority,
// repository and token ports belong to the access module.
//
// # Outputs
//
// Expiring process-local sessions and approvals, current-authority checks
// and single-winner P4 token delivery; no password or issued token retained
// in approval state and no metadata transaction across authentication I/O.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 286, and "Access and identity", line 548.
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
// Implemented for #262. External identity providers, MFA challenges and
// shared HA sessions retain their separate P21/P35 owners.
package session
