// Package sqlite opens the composite SQL store on SQLite: the pool of the
// platform/sql/sqlite driver, migrated and wrapped in the composite store
// of app/adapters/sql.
//
// # Role
//
// app, adapter layer (SQL on SQLite): opens the composite on one driver with
// the options and error texts it had before the composite moved under app.
// app's store opener calls it for the sqlite database type; the tests of
// the modules reach it only through app/storetest's OpenSQLite. It imports
// app/adapters/sql and the SQLite driver.
//
// # Inputs
//
//   - Open: the path of the database file, or the driver's Memory path,
//     and Options: the composite store's Options and the driver's busy
//     timeout.
//
// # Outputs
//
//   - Open: the composite store over the database, created if it does not
//     exist, migrated unless Options.SkipMigrate.
//
// # Invariants
//
//   - The driver's errors, ErrPathRequired among them, are returned as they
//     are, and the composite's open errors behind "sqlite: ", so the texts
//     that operators see and the wrapcheck exception for the driver's OpenDB
//     stay as they were.
//   - On any failure the pool is closed and no store is returned.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (SQLite, the default metadata database, and PostgreSQL behind one dialect
//     layer).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence"; "Engine drivers").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 751 (app's platform.go opens and migrates the SQL store).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/995
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P2 (pull request 3). Moved here in P9d: the pool comes from the
// platform/sql/sqlite driver (issue 995), and the package moved under app
// (issue 1098).
//
// # Testing
//
//   - sqlite_test.go: TestAccContractFile and TestAccContractMemory run the
//     composite contract of app/storetest on a file and in memory;
//     TestAccMigrationsUpDownUp, TestAccLegacyHistoryRefused,
//     TestAccOpenErrors, TestAccKeyRotation, TestAccRemoteCachePersistence,
//     TestAccRepositoryHeaderSecrets and TestAccRepositoryHeaderMigration
//     run its composite suites.
//   - schema_test.go: TestAccSchemaGolden compares the migrated schema with
//     testdata/schema.golden.
//   - replay_test.go: TestAccReplayProcessCrash kills a child process,
//     TestAccReplayCrashHelper, after its write and before the write's replay
//     receipt commits, and checks that both roll back;
//     TestAccKeyedReplayProcessCrash, with its child
//     TestAccKeyedReplayCrashHelper, does the same under the shared replay
//     of keyed operations, inside its effect.
package sqlite
