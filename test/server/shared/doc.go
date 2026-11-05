// Package shared runs the authentication rules that every route of the
// assembled server shares: which routes are public, and the status, challenge
// and message of a request without credentials or with bad ones.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. Its owner in
// tools/citestselection/test_ownership.json is shared, so a change to it
// selects full CI.
//
// # Inputs
//
//   - The production router from internal/server over a seeded in-memory
//     store whose admin password is "pw".
//
// # Outputs
//
//   - TestAccAuthentication: the status, challenge and error message of each
//     case.
//
// # References
//
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P3; P9b moved the tests here from internal/server (issue 673).
package shared
