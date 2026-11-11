// Package saml will be the SAML identity-provider adapter of the access
// module.
//
// # Role
//
// access module, adapter layer (SAML).
//
// # Inputs
//
// None yet. Planned: SAML settings and the assertions that identity providers
// return (plan P21, task 2).
//
// # Outputs
//
// None yet. Planned: verified identities for access/core/federated, rejecting
// bad signatures, issuers and audiences (plan P21, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 299, and "Access and identity", line 557.
//   - Plan:
//     docs/implementation/plan.md#p21-browser-and-enterprise-authentication.
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
// Placeholder: implemented by P21.
package saml
