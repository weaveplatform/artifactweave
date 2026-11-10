// Package postgres opens the composite SQL store on PostgreSQL: the pool of
// the platform/sql/postgres driver, migrated and wrapped in the composite
// store of app/adapters/sql.
//
// # Role
//
// app, adapter layer (SQL on PostgreSQL): opens the composite on one driver
// with the options and error texts it had before the composite moved under
// app. app's store opener calls it for the postgres database type; the
// tests of the modules reach it only through app/storetest's OpenPostgres.
// It imports app/adapters/sql and the PostgreSQL driver.
//
// # Inputs
//
//   - Open: the DSN of the database, and Options: the composite store's
//     Options and the driver's pool settings, the open connection limit and
//     the connection lifetime.
//
// # Outputs
//
//   - Open: the composite store over the database, migrated unless
//     Options.SkipMigrate.
//
// # Invariants
//
//   - The driver's errors, ErrDSNRequired among them, are returned as they
//     are, and the composite's open errors behind "postgres: ", so the texts
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
// Implemented P2 (pull request 3). Staged in P9d: the pool comes from the
// platform/sql/postgres driver (issue 995), and the package moved under app
// (issue 1098).
//
// # Testing
//
// Every test but TestAccOpenErrors runs on the PostgreSQL server that the test
// binary shares (platform/sql/sqltest), and skips with it.
//
//   - postgres_test.go: TestAccContract runs the composite contract of
//     app/storetest, emptying the database between stores with its Reset;
//     TestAccKeyRotation, TestAccLegacyHistoryRefused,
//     TestAccRemoteCachePersistence, TestAccRepositoryHeaderSecrets and
//     TestAccRepositoryHeaderMigration run its composite suites;
//     TestAccStreamsMigrateAlone migrates each module's stream up and down on
//     its own, in a scratch schema; TestAccOpenErrors opens an empty DSN, a
//     malformed one and a closed port, which need no server.
//   - schema_test.go: TestAccSchemaGolden compares the migrated schema with
//     testdata/schema.golden.
//   - replay_test.go: TestAccReplayProcessCrash, with its child
//     TestAccReplayCrashHelper, as on SQLite, and
//     TestAccReplayPruneDoesNotDeleteRenewal.
package postgres
