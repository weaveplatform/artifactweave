// Package session will hold browser sessions and native-client approval
// flows: sessions, CSRF protection, approvals and PKCE.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// None yet. Planned: browser logins and the approval requests of native
// clients, such as npm web login and Terraform login.v1 with PKCE (plan P21,
// task 1).
//
// # Outputs
//
// None yet. Planned (target layout, line 548): session.go, csrf.go,
// approval.go and pkce.go: sessions with consent, expiry and logout, CSRF
// protection, and scoped credentials for approved clients.
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
// Placeholder: implemented by P21.
package session
