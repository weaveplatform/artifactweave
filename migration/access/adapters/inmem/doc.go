// Package inmem is the in-memory implementation of the access module's
// stores: users, groups and their memberships, permissions, tokens, key
// pairs, certificates, trusted and signing keys, projects with their roles,
// environments, members and repository assignments, and API restrictions.
//
// # Role
//
// access module, adapter layer (in-memory): the reference implementation of
// the module's port.Store over a shared memtx.Store. The composite
// in-memory store in app/adapters/inmem builds one Store over its own
// memtx.Store and delegates Users, Groups, Permissions, Tokens, Keys,
// Projects and APIRestrictions to it. The package imports the standard
// library, the kernel, the platform and the module's core and api only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Users, Groups, Permissions, Tokens, Keys,
//     Projects and APIRestrictions, and memtx's Tx, AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (users, user_groups, group_members, permissions, tokens,
//     keypairs, certificates, trusted_keys, signing_keys, projects, roles,
//     environments, project_members, project_repositories, api_restrictions
//     and api_restriction_audit), so every call joins the transaction that
//     the caller's context carries, whichever module's store began it. A
//     permission holds its grants and targets, and an assignment the
//     projects it is shared with, which the SQL store keeps in tables of
//     their own.
//   - Secrets are kept as given: nothing here outlives the process, so
//     there is nothing to seal.
//   - A name filter folds case when it reads, with strings.ToLower, as the
//     SQL store's search names are folded when they are written.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 296 and 297 (the access adapters) and line 1056 (the metadata
//     store's in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p4-identity-and-authorization,
//     #p11-projects-webhooks-retention-replication and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1060
//
// # Status
//
// Implemented P2: the user, group, permission, token, key and project
// stores, as the metadata store's (pull request 3). The identity listings
// and the API restriction store came with retry safety and bounded API
// execution (pull request 905). Staged in P9d: Store and New.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the access contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
