// Package port declares the access module's store ports: users, groups,
// permissions, tokens, keys, projects and API restrictions, the values
// their listings take and return, and Store, every store the module owns.
//
// # Role
//
// access module, core layer, store ports. The module's adapters implement
// them: adapters/sql, which seals the secret columns, and adapters/inmem.
// The metadata store's SQL and in-memory stores delegate their user, group,
// permission, token, key, project and API restriction accessors to those
// adapters, so both satisfy Store. The ports take and return the module's
// core/model values, its api's key pairs, certificates and resource types,
// and kernel.Page.
//
// # Inputs
//
//   - Users, groups and their memberships, permissions, token records, key
//     pairs, certificates, trusted and signing keys, projects with their
//     roles, environments, members and repository assignments, and API
//     restrictions, to store; names, ids, digests and keys to look up.
//   - An IdentityFilter, UserFilter or TokenFilter with a kernel.Page to
//     narrow a listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - UserStore, GroupStore and PermissionStore; TokenStore with DigestKind
//     and its Digest* constants; KeyStore; ProjectStore; and
//     APIRestrictionStore.
//   - IdentityFilter, IdentityResource, UserFilter, UserSummary,
//     TokenFilter, and APIRestriction with Validate.
//   - Store: tx.Runner, Users, Groups, Permissions, Tokens, Keys, Projects
//     and APIRestrictions.
//
// # Invariants
//
//   - Every store joins the caller's unit of work: a call made with a
//     context inside Tx is part of that transaction, and is rolled back
//     with it.
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrConflict for a
//     name, id, digest or alias that exists, kernel.ErrNotFound for a
//     missing record or a named group, member or project that does not
//     exist, and kernel.ErrInvalid for an API restriction the stores cannot
//     hold.
//   - Secrets are given and returned in plain text, and sealed at rest
//     (decision 0017): key pairs' private keys and passphrases, signing
//     keys' private keys and passphrases, and a purposed token's secret.
//     Other tokens are never stored, only their digests.
//   - A user summary leaves out password hashes, credentials and
//     memberships, so a listing cannot leak them.
//   - API restriction changes append to an audit that outlives the user.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md
//     (permissions mirrored by principal and by target).
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence" and "Names the migration introduces", which lists
//     Store).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 272 to 276 (the access module's core/port/store.go), lines 296
//     and 297 (its sql, inmem and storetest adapters), lines 577 and 578
//     (each module owns its store ports) and line 1055 (the metadata store
//     split into each module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1,
//     #p4-identity-and-authorization and #p9d-modular-monolith-migration,
//     epic STO (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1054
//
// # Status
//
// Implemented P2: UserStore, GroupStore, PermissionStore, TokenStore,
// KeyStore and ProjectStore, with the metadata store's in-memory, SQLite
// and PostgreSQL stores (pull request 3). The identity listings and
// APIRestrictionStore came with retry safety and bounded API execution
// (pull request 905). Staged in P9d, where Store is declared.
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// module's contract (adapters/storetest), with its Users, UserLockout,
// Groups, Memberships, Permissions, IdentityPages, Tokens, Keypairs,
// Certificates, TrustedKeys, SigningKeys, Projects, Roles, Environments,
// ProjectMembers, Assignments, APIRestrictions and
// APIRestrictionConcurrentAudit subtests, runs the ports on the module's
// in-memory and SQL adapters and on the metadata store's in-memory, SQLite
// and PostgreSQL stores. Each factory returns a Store, so the tests compile
// only while every one of those stores satisfies it.
package port
