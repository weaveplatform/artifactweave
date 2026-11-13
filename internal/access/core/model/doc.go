// Package model holds the access module's entity values: users and groups,
// permissions, roles, projects and environments, access tokens, trusted and
// signing keys and the access policies, with the conversions between the
// account and permission values and JFrog's wire forms.
//
// # Role
//
// access module, core layer, entity values. The module's store ports take
// and return them, and the access services build on them. The access api
// publishes only the vocabulary other modules share (permission actions and
// resource types, key pairs and certificates, token shapes); everything
// else access persists is here (decision 0044, "Ownership of shared
// models"). The package imports the standard library, its own module's api
// (that vocabulary), the kernel (token values and scopes) and kernel/wire
// (JFrog's wire structs).
//
// # Inputs
//
//   - For the conversions, JFrog's wire forms of users, groups and
//     permissions, as kernel/wire declares them: the legacy security API's
//     (permission targets v1 and v2) and Access's.
//   - For the checks and parsers, a project key, an environment name, a role
//     action, a token subject or a user name.
//
// # Outputs
//
//   - User, Group, UserStatus, MFAStatus, RoleFlags, RealmInternal and
//     EffectiveAdmin; UserFromLegacy, UserToLegacy, UserFromAccess and
//     UserToAccess, the same four for Group, and the sentinels
//     ErrInvalidTimestamp and ErrInvalidStatus.
//   - Permission, Grants, Target, Resource and MatchesRepository;
//     PermissionFromV1, PermissionToV1, PermissionFromV2, PermissionToV2,
//     PermissionFromAccess and PermissionToAccess, and the sentinel
//     ErrLossyProjection.
//   - Project, ProjectMember, RepositoryAssignment, AdminPrivileges, Role
//     with RoleType, Environment and PrincipalType; ValidateProjectKey,
//     ProjectRepoKey and the sentinel ErrInvalidProjectKey; and the role and
//     environment vocabulary: GlobalRoleActions, IsGlobalRoleAction,
//     MaxCustomGlobalRoles, BuiltinGlobalRoles, BuiltinGlobalRole,
//     RoleActions, IsBuiltinEnvironment and
//     ValidEnvironmentName.
//   - Token, Revocable, Subject and ParseSubject with the sentinel
//     ErrInvalidSubject, the token defaults and
//     TokenPurposeEncryptedPassword.
//   - TrustedKey and SigningKey.
//   - FoldUsername, SameUsername and FoldUsernames, the user name rule, and
//     FoldUserGrants with Resource.FoldGrants and Permission.FoldGrants, its
//     application to a permission's user grants.
//   - UserLockPolicy, PasswordExpirationPolicy, PasswordPolicy with
//     DefaultPasswordPolicy, and AccessLockSettings with
//     DefaultAccessLockSettings.
//
// # Invariants
//
//   - A conversion to a wire form that cannot express a stored permission
//     fails with ErrLossyProjection, rather than grant more or less than the
//     permission does (decision 0006).
//   - Secrets held in memory (password hashes, token digests, a purposed
//     token's secret, a signing key's private key and passphrase) are tagged
//     json:"-", so no document column can hold one; the stores keep the
//     sealed ones in sealed columns (decision 0017).
//   - A user name is stored and matched folded to lower case (FoldUsername),
//     as Access treats user names: "User names are case-insensitive" (JFrog's
//     SCIM page), and RTFACT-29660 (7.55.13) stores a user created "with
//     capitalization" in lower case. A group name keeps its case (#1309).
//   - The string constants (statuses, role types, principal types, the
//     built-in roles and environments) are stored and on the wire, so their
//     values never change.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (tokens, their defaults and subjects).
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md
//     (permissions and their wire forms).
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md
//     (projects, roles and environments).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the sealed secrets).
//   - Decision 0020: docs/research/decisions/0020-domain-model.md (one
//     internal value per entity, converted to and from the wire forms).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Ownership of shared models").
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//     Research 07: docs/research/07-webhooks-retention-projects.md (projects
//     and roles).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 272 to 276 (the access module's core/model and core/port),
//     lines 577 and 578 (each module owns its store ports) and line 1058
//     (the shared types split into each module's core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0,
//     #p4-identity-and-authorization and #p9d-modular-monolith-migration,
//     epic STO (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1054
//
// # Status
//
// Implemented P2: the access values among the shared domain types, with
// their wire conversions (pull request 3). P4 added the global roles and
// environments (pull request 93) and the encrypted-password token purpose
// (pull request 108), and AccessLockSettings came with the Access user lock
// policy endpoints (pull request 242). Moved here in P9d. The user name rule
// came with #1432.
//
// # Testing
//
//   - permconv_test.go: the permission document, MatchesRepository, and the
//     round trips through the v1, v2 and Access forms, with the defaults and
//     the refusals of lossy projections.
//   - userconv_test.go: the user and group round trips through the legacy
//     and Access forms, their defaults and status mapping, EffectiveAdmin
//     and a user who never logged in.
//   - roles_test.go: the global role vocabulary, the built-in roles,
//     environment names, RoleActions and the developer default.
//   - model_test.go: TestUnitToken (Expired, Revocable, the default expiry
//     and ParseSubject), TestUnitProject (ValidateProjectKey and
//     ProjectRepoKey) and TestUnitSigningKeyJSON (a signing key's secrets
//     stay out of its JSON).
//   - username_test.go: TestUnitFoldUsername and TestUnitFoldGrants.
//
// The module's contract suite, internal/access/adapters/storetest.Run, stores
// and reads these values in its identity, token, key and project subtests;
// the access adapters run it in memory and on SQLite, and the metadata
// store's contract in app/storetest runs it on its in-memory, SQLite and
// PostgreSQL stores.
package model
