// Package access runs the access module's rules on the assembled server: the
// authentication every route shares, which routes are public, and the
// status, challenge and message of a request without credentials or with bad
// ones.
//
// # Role
//
// Test support: the server-assembly suites of the access module, which drive
// the production router through HTTP requests. The package's owner in
// tools/citestselection/test_ownership.json is shared, so a change to it
// selects full CI.
//
// # Inputs
//
//   - The production router from test/server/support/harness, over an
//     in-memory store seeded by test/server/support: the admin user, whose
//     password is "pw", and the anonymous access setting, which a case turns
//     on.
//
// # Outputs
//
//   - Assertions only. TestAccAuthentication checks the status, challenge
//     and error message of each case, the error envelope of the /access
//     routes, and what a request without credentials reaches once anonymous
//     access is on.
//
// # References
//
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": the authentication suite belongs to
//     the access module's suites, not to test/server/support).
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P3; P9b moved the tests out of internal/server (issue 673).
package access
