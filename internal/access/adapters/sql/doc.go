// Package sql is the SQL implementation of the access module's stores:
// users, groups and their memberships, permissions, tokens, key pairs,
// certificates, trusted and signing keys, projects with their roles,
// environments, members and repository assignments, and API restrictions,
// with the module's registration with the SQL layer.
//
// # Role
//
// access module, adapter layer (SQL). Store implements the module's
// port.Store (port.UserStore, port.GroupStore, port.PermissionStore,
// port.TokenStore, port.KeyStore, port.ProjectStore and
// port.APIRestrictionStore over the shared unit of work) over a shared
// dialect.Store, on SQLite and PostgreSQL alike. Module registers the access
// stream (adapters/sql/migrations), its six sealed columns and the
// identity-search backfill, third in the canonical module order. The
// composite SQL store in app/adapters/sql builds one Store over its own
// dialect store, delegates Users, Groups, Permissions, Tokens, Keys,
// Projects and APIRestrictions to it and names Module in its module list.
// The package imports the standard library, the kernel, the platform, the
// module's core and api and its stream only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the access stream's
//     tables, with the keyring that seals its secret columns, or none.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Users, Groups, Permissions, Tokens, Keys,
//     Projects and APIRestrictions, and the dialect store's Tx, AfterCommit
//     and Close.
//   - Module: the name access, the stream migrations.FS, the six sealed
//     columns and the AfterMigrate hook.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence"). That is what
//     lets deleting a key pair clear the repositories that name it, and
//     renaming an environment rename it in repository configurations, in
//     the transaction that changes the access rows.
//   - Every column the store seals is in Module: keypairs.private_key and
//     keypairs.passphrase under the key pair's name, signing_keys.private_key
//     and signing_keys.passphrase under the signing key's alias,
//     certificates.private_key under the certificate's alias and
//     tokens.secret under the token's id, each with its table.column as the
//     purpose, so key rotation re-seals them (decision 0017). Neither the
//     purpose nor the row id ever changes: both are authenticated with every
//     value.
//   - AfterMigrate fills the lowercase search names that users and groups
//     lack, one bounded page per transaction, folding case in Go because the
//     engines fold it by their own rules (decision 0043). The metadata
//     store's open runs it after the artifacts module's repository header
//     backfill, and before the server serves.
//   - The SQL text, the table names, the purpose strings, the row ids and
//     the error texts are those the metadata store's SQL store used before
//     the module owned them.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md (the
//     identity listings and their search names).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order,
//     backfills as AfterMigrate hooks; "Names the migration introduces",
//     which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 296 and 297 (the access adapters), lines 614 and 615 (the access
//     migrations) and lines 631 to 633 (RunModule).
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
// execution (pull request 905). Moved here in P9d, which added Store, New and
// Module.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the access contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - identity_test.go: TestAccIdentityFaults (a statement that fails
//     anywhere inside an identity call surfaces as an error, never as a
//     store sentinel) and TestAccSealedIdentityColumns (key material is
//     sealed at rest, and a store without the keyring cannot read it).
//   - identity_query_test.go: TestAccIdentityPagingAtScale (first and late
//     pages over 5000 users, and an index seek for the late page, on SQLite
//     and on PostgreSQL) and TestAccIdentitySearchUpgrade (AfterMigrate
//     fills missing search names, and a second run changes nothing).
//   - projects_test.go: TestAccProjectsFaults (a fault injected after a
//     successful statement fails the project call) and
//     TestAccProjectsUnmigrated (every project call reports the driver's
//     error on a database without the schema).
package sql
