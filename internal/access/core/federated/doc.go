// Package federated will link external identities to local subjects: account
// linking, group and role mapping, the MFA policy and local-admin recovery.
//
// # Role
//
// access module, core layer. The identity-provider protocols are the saml,
// oidcidp, crowd and httpsso adapters; this package holds the rules they
// share.
//
// # Inputs
//
// None yet. Planned: the identities that the SAML, human OIDC, Crowd and
// HTTP-SSO adapters assert (target layout, line 557).
//
// # Outputs
//
// None yet. Planned (target layout, line 549): linking.go, mapping.go, mfa.go
// and recovery.go: linked accounts, mapped groups and roles, the MFA policy
// decision and local-admin recovery.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 287, and "Access and identity", line 549.
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
package federated
