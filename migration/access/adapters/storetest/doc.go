// Package storetest is the contract suite of the access module's stores:
// one set of expectations that every implementation of the user, group,
// permission, token, key, project and API restriction stores must meet.
//
// # Role
//
// access module, adapter layer, test support. The module's SQL and
// in-memory adapters run Run with a Factory that returns a fresh store; the
// metadata store's contract runs it too, so its in-memory, SQLite and
// PostgreSQL stores pass the same cases. The package imports the standard
// library, the kernel, the platform and the module's core and api only.
//
// # Inputs
//
//   - Run: a Factory, which returns a fresh, empty port.Store and registers
//     its cleanup with t.
//
// # Outputs
//
//   - Factory and Run.
//   - The subtests, one per concern, each on a store of its own:
//     IdentityPages, Users, UserLockout, Groups, Memberships, Permissions,
//     Tokens, Keypairs, Certificates, TrustedKeys, SigningKeys, Projects,
//     Roles, Environments, ProjectMembers, Assignments, APIRestrictions and
//     APIRestrictionConcurrentAudit.
//
// # Invariants
//
//   - The subtests keep the names they had in the metadata store's
//     contract, which calls Run where it ran IdentityPages, so its runs list
//     the same subtests.
//   - The subtests use only the access accessors, so a store that holds
//     nothing but the access stream's tables passes them.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite per store port).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": contract suites; "Names the migration introduces",
//     which lists Factory and Run).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 297 (the access storetest adapter), line 847 (store contract
//     suites per module, run by its adapters) and line 1057 (the metadata
//     store's contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1060
//
// # Status
//
// Implemented P2: the identity, token, key and project cases, in the
// metadata store's contract (pull request 3). The identity listing and API
// restriction cases came with retry safety and bounded API execution (pull
// request 905). Staged in P9d: Factory and Run.
//
// # Testing
//
// The package is the suite. access/adapters/inmem's TestUnitContract and
// access/adapters/sql's TestAccContract (SQLite in memory and in a file)
// run Run, and so do the metadata store's TestUnitContract,
// TestAccContractFile, TestAccContractMemory and TestAccContract
// (PostgreSQL).
package storetest
