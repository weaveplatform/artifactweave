// Package identity manages internal users and groups and the account
// policies (decision 0005): creation and replacement with hashed passwords,
// group membership and auto-join, the user lock and password expiration
// policies, locks and forced password changes. The legacy security API and
// Access v2 serve it in their own shapes.
//
// # Role
//
// access module, core layer: the identity service. The legacy security API
// handlers (/artifactory/api/security/users, groups, locks and policies) and
// the Access v2 user and group handlers in access/adapters/http build a
// Service over the configured store and call it; other modules reach users and
// groups only through access/api. The package imports the standard library,
// the kernel, platform/settings, and the module's core/authn (the password
// hash and the lock and expiration policy reads), core/model and core/port.
//
// # Inputs
//
//   - Service: a Store (the access ports with their unit of work, and the
//     settings store), which the metadata store satisfies; the password
//     hash cost; a clock.
//   - Users and groups in model form with clear-text passwords, partial
//     updates (UserPatch, GroupPatch), membership add and remove lists, and
//     the lock, expiration, password and Access lock policies.
//
// # Outputs
//
//   - Users and groups in the access stores, with argon2id password hashes
//     and the auto-join groups a new user joins; the policies in the
//     settings store; locks cleared, passwords expired and the encrypted
//     passwords that follow a changed password revoked.
//   - ErrInvalid (with ErrProtected for a change to a built-in principal)
//     and ErrExists for a refused request; store errors, kernel.ErrNotFound
//     among them, come back for the handlers to map.
//
// # Invariants
//
//   - A change that takes several store calls runs in one transaction of
//     the Store, so a write that fails part way leaves nothing behind.
//   - The last administrator cannot be deleted or demoted, and the seeded
//     admin and anonymous users cannot be deleted.
//   - A changed or expired password revokes the user's encrypted password,
//     so a value issued for the old password stops working.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md
//     (internal users and passwords are the first tier).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (modules reach each other through api packages; module stores).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 280 (core/identity) and line 1028 (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1065
//
// # Status
//
// Implemented P4: the legacy security API's users, groups and policies
// (pull request 97), Access v2 users and groups and the password policy
// (pull request 105), the encrypted password tied to the password (pull
// request 108), the Access user lock policy (pull request 242), the legacy
// password endpoints (pull request 466) and login suspension, filters and
// limits (pull request 470). Moved here in P9d, where Service takes Store.
// Access v2's create or replace of a group (ReplaceGroup) came with issue
// 1200.
//
// # Testing
//
//   - identity_test.go: TestUnitPutUser, TestUnitPatchUser,
//     TestUnitLastAdminAndDeletion, TestUnitGroups, TestUnitLocksAndExpiry,
//     TestUnitPolicies, TestUnitAccessLockPolicy,
//     TestUnitEncryptedPasswordFollowsThePassword, TestUnitChangePassword
//     and TestUnitChangePasswordRaces.
//   - access_test.go: TestUnitCreateOnly, TestUnitMemberships,
//     TestUnitPasswordPolicyRules, TestUnitBulkPasswordExpiry,
//     TestUnitBulkExpiryRevokesEncryptedPasswords and
//     TestUnitAccessStoreFailures.
//   - fault_test.go: TestUnitStoreFailures and
//     TestUnitAccessLockPolicyStoreFailures.
//   - replace_group_test.go: TestUnitReplaceGroupCreates,
//     TestUnitReplaceGroupResetsEachField, TestUnitReplaceGroupSetsEachField,
//     TestUnitReplaceGroupRefusals and TestUnitReplaceGroupOneTransaction.
//
// The tests run on the metadata store's in-memory store, seeded with the
// built-in principals through a stand-in for the artifacts module's
// repositories (repositories_test.go).
package identity
