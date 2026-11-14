// Package storetest is the contract suite of the composite stores and the
// suites that need a whole composite: Run holds every backend of the
// metadata store (in memory, SQLite in memory and in a file, PostgreSQL) to
// one set of expectations, and the SQL suites check key rotation, sealed
// repository headers, layout fencing across a reopen, a crashed replayed
// write and the refusal of a legacy migration history on both SQL
// composites. It also gives the tests of the modules the composite SQL
// store's type and openers.
//
// # Role
//
// app, test support: the composite stores' counterpart of each module's
// adapters/storetest. A module's tests may import test support from app and
// nothing else (dependency rule 1), so SQLStore, Options, SQLiteOptions,
// PostgresOptions, OpenSQLite and OpenPostgres are how they name and open
// the SQL composite. Production code never imports it. It imports the
// composite SQL store and its dialect packages, every module's port and
// adapters/storetest, the platform's store suite and SQL harness, storekit,
// and the roles and repository services that
// RepositoryHeaderEnvironmentRename drives.
//
// # Inputs
//
//   - Run: a Factory that returns a fresh, empty composite store and
//     registers its cleanup with the test.
//   - Rotation, RepositoryHeaderEnvironmentRename and RepositoryHeadersOpen:
//     an Opener that opens the same database on each call with the keyring
//     it is given.
//   - RemoteLayoutPersistence: an open SQL composite and a function that
//     opens the same database again.
//   - ReplayCrash and KeyedReplayCrash: a store and the environment that
//     makes the backend's child test, which calls ReplayCrashChild or
//     KeyedReplayCrashChild, open the same database.
//   - LegacyHistoryRefused: an empty database, its dialect and a function
//     that opens a store over it with migrations. Reset: a database and its
//     dialect.
//   - OpenSQLite and OpenPostgres: a path or a DSN and the options of the
//     composite store and its driver.
//
// # Outputs
//
//   - Run: a subtest per concern: the transactions, one of them across two
//     modules' stores, and the pagination input every store builds on,
//     each module's contract and the platform's,
//     the layout fence that needs the platform's config store and the
//     artifacts module's remote sources together, and the build row lock.
//   - Opener, Rotation, RepositoryHeaderEnvironmentRename,
//     RepositoryHeadersOpen, RemoteLayoutPersistence, ReplayCrash,
//     ReplayCrashChild, KeyedReplayCrash, KeyedReplayCrashChild and
//     LegacyHistoryRefused: failures of the calling
//     test, each naming the expectation it breaks.
//   - Reset: the database emptied, by rolling back and applying again every
//     stream that the composite's module list registers.
//   - SQLStore and Options, the composite SQL store and its options;
//     SQLiteOptions and PostgresOptions; OpenSQLite and OpenPostgres, which
//     open the composite as the server does.
//
// # Invariants
//
//   - It never imports app, which imports every module, so a test of a
//     module may import it without an import cycle unless the test's
//     package is one that this package imports itself: a module's port,
//     adapters and storetest, which the composites import, and the roles
//     and repository services.
//   - Each case of Run gets a store of its own from the factory, so no case
//     sees another's rows.
//   - OpenSQLite and OpenPostgres are the server's opens: the database is
//     migrated unless Options.SkipMigrate, the pool is closed on failure,
//     and the error texts are the server's.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite for every backend).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (key rotation and sealed columns).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence"; "Dependency rules", rule 1; "Names the migration
//     introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 847 (each module's store contract suite in its
//     adapters/storetest).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P2: the contract suite and key rotation (pull request 3).
// Later phases added the layout persistence suite (pull request 646), the
// repository header suites (pull request 656), the replay crash (pull
// request 905) and the legacy history refusal (pull request 1165). Moved here
// in P9d: each module's part of the contract moved to its adapters/storetest
// (issues 1056 to 1062), and issue 1098 merged the composite contract, the
// composite SQL suites and the openers here. P9d's final qualification added
// the transaction across two modules' stores, which epic STO (issue 939)
// asks for.
//
// # Testing
//
// It has no tests of its own: app/adapters/inmem runs Run on the in-memory
// composite, and app/adapters/sql/sqlite and app/adapters/sql/postgres run
// Run and every SQL suite on theirs. Its remaining branches are t.Fatal
// paths taken only when a backend breaks the contract.
package storetest
