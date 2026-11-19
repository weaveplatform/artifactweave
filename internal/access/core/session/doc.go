// Package session implements native-client PKCE and callback validation and
// will hold browser sessions, CSRF protection and approvals.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// PKCE challenges/verifiers and loopback callbacks from native clients.
// Planned: browser logins and approval requests for npm web login and
// Terraform login.v1 (plan P21, task 1).
//
// # Outputs
//
// S256 proof verification and strict validation of the pinned Terraform
// client's localhost /login callback. Planned (target layout, line 548):
// session.go, csrf.go and approval.go: sessions with consent, expiry and logout, CSRF
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
// PKCE and callback primitives implemented for issue 262. Browser approval,
// sessions, token delivery and HTTP/native acceptance remain unfinished.
package session
