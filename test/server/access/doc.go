// Package access runs the access module's rules on the assembled server:
// authentication and which routes are public; access tokens and their
// lifecycle; users, groups and the account policies in the legacy security
// API and Access; permissions in the v1, v2 and Access APIs; global roles and
// environments; key pairs and certificates; the OIDC token exchange; and the
// API restrictions that stop a user's expensive requests.
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
//   - The worlds of fixture_test.go, which keep the names of the router
//     guards' fixtures in internal/app: the harness's item world
//     (newItemWorld); the guards' status fixture's users, repositories, key
//     pairs, permissions, items and tokens (statusFixture), without what only
//     the format, build, search, overwrite and multipart rows need; the token
//     fixture (tokenFixture); and the identity world (newIdentityWorld). The
//     router's paths and limits that the tests name are constants there,
//     spelled as the router and its handlers spell them.
//
// # Outputs
//
// Assertions only, one file per area:
//   - auth_test.go: the status, challenge and error message of each
//     authentication case, the error envelope of the /access routes, and
//     what a request without credentials reaches once anonymous access is on.
//   - access_test.go: path-level permissions, scoped token isolation,
//     administrators through a group, and the permissions view's patterns.
//   - tokens_test.go and token_auth_test.go: creating, listing, refreshing
//     and revoking tokens in Access and the legacy API, transient users and
//     the encrypted password, and authentication by token.
//   - security_test.go and access_identity_test.go: users, groups, locks,
//     password expiry and policies, login suspension and the last
//     administrator, in the legacy security API and Access.
//   - permissions_test.go: one permission in three APIs, MANAGE holders, and
//     the Terraform provider's flows.
//   - roles_test.go and roles_status_test.go: global roles and environments,
//     and the global environment limit.
//   - keypairs_test.go: key pairs and certificates, with the Terraform
//     provider's flow.
//   - oidc_test.go: the OIDC exchange, self-minting, mappings and restricted
//     subjects.
//   - api_restrictions_test.go: restriction, recovery, validation and the
//     project quota.
//   - username_case_test.go: user names in any case on every route that
//     takes one (#1432), and an unknown group or member in a user or legacy
//     group request (#1433), on the in-memory, SQLite and PostgreSQL stores.
//   - ignored_basic_test.go: the Basic values 7.x reads no credential from,
//     served as a request without credentials and counted as no failed
//     login, and the credentials handed back beside them (#1451).
//   - builtin_user_delete_test.go: the legacy refusal to delete a built-in
//     user, worded without the identity service's sentinels (#1449).
//
// # References
//
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": the authentication suite belongs to
//     the access module's suites, not to test/server/support).
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1066
//
// # Status
//
// Implemented P3 (authentication) and P4 (tokens, users and groups,
// permissions, roles and environments, key pairs, OIDC), with the API
// restrictions of decision 0043 in P9c. P9b moved the authentication tests out
// of the server root (issue 673), and P9d the rest (TST-04).
package access
