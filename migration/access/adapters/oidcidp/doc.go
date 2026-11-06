// Package oidcidp will be the adapter for human sign-in through an OIDC or
// OAuth identity provider.
//
// # Role
//
// access module, adapter layer (OIDC and OAuth).
//
// # Inputs
//
// None yet. Planned: OIDC provider settings and the tokens a provider issues
// at human sign-in (plan P21, task 2).
//
// # Outputs
//
// None yet. Planned: verified identities for access/core/federated. Workload
// OIDC exchange stays in access/core/oidc and does not implement human login
// (decision 0019).
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
package oidcidp
