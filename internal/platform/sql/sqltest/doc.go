// Package sqltest is the SQL harness that runs a module's store suite on
// each dialect: RunModule hands the suite fresh databases, on SQLite in
// memory and in a file and, when asked, on PostgreSQL, each migrated with the
// module's streams alone. It also starts the one PostgreSQL server that a
// test process shares, holds the statements and checks that the SQL fault
// tests share, and describes a database's schema for the golden files.
//
// # Role
//
// platform layer, test support (platform/sql): the SQL harness that runs a
// module's store suite on each dialect, so a module adds one test file to get
// its SQLite runs, and its PostgreSQL run when it asks for one. Test files in
// any module may import it (dependency rule 1); production code never does.
// It imports the dialect layer, the SQLite and PostgreSQL drivers,
// platform/crypt, testcontainers' PostgreSQL module, pkg/containertest and
// go-cmp.
//
// # Inputs
//
//   - For RunModule: the modules whose streams make a store's schema, in the
//     canonical module order; open, which builds the module's store over a
//     dialect.Store; the suite; and the options WithPostgres and WithKeyring.
//   - For OpenSQLite: the modules. For Reset: a database, its dialect and the
//     modules whose tables to empty. For Schema: a database and its
//     dialect's name; for SchemaGolden, also the golden file.
//   - ARTIFACTWEAVE_TEST_POSTGRES_DSN, a PostgreSQL server to use in place of
//     a container.
//   - Driving: the module store suites from STO-12 to STO-18, and their fault
//     tests; the composite store's suites in app, which call PostgresDSN,
//     Schema and SchemaGolden, and reach Reset through app/storetest's Reset
//     over the composite's module list.
//
// # Outputs
//
//   - RunModule, with Option, WithPostgres and WithKeyring: the subtests
//     memory, file and, when asked for, postgres, in each of which the suite
//     gets a newStore that opens a fresh store.
//   - OpenSQLite: one fresh in-memory store.
//   - PostgresDSN, the DSN of the shared server, and ScratchDSN, a DSN whose
//     connections start in an empty schema of their own.
//   - Reset: the tables of the given modules emptied.
//   - Exec, Fails, Err, Err3, FailTrigger and DropTable: the fault-test
//     helpers.
//   - Schema, a description of every table, index, constraint and sequence
//     and the row count of every table that holds rows, with
//     ErrUnknownDialect; SchemaGolden, which compares it with a golden file,
//     rewrites the file when asked, and proves that an added table changes
//     the description.
//
// # Invariants
//
//   - A test process starts at most one PostgreSQL container: PostgresDSN
//     starts it on its first call, and every PostgreSQL run of the process
//     shares it. The image is pinned to postgres:17-alpine so that a
//     PostgreSQL release cannot change behaviour under the suites; a bump is
//     deliberate.
//   - Unless ARTIFACTWEAVE_TEST_POSTGRES_DSN names a server, PostgresDSN skips
//     the calling test in a -short run and where no Docker engine that runs
//     Linux containers answers, so the suites run everywhere and their
//     PostgreSQL half runs in the Linux CI jobs (decision 0015).
//   - Every store that RunModule or OpenSQLite hands out has a database of its
//     own, holding only the history and the tables of its modules' streams: a
//     new SQLite database in memory or in a file of the test's temporary
//     directory, or, on PostgreSQL, a scratch schema of its own on the shared
//     server. A row written through one store never reaches another; the
//     composite tables that other tests of the process keep in the server's
//     public schema are left alone; and test processes that share a server
//     through ARTIFACTWEAVE_TEST_POSTGRES_DSN do not meet in it.
//   - A store opens as the composite store opens: a ping, every stream
//     migrated, the pool wrapped with the keyring, then each module's
//     AfterMigrate in module order. The keyring is the one the composite
//     store's runs seal with, unless WithKeyring replaces it.
//   - A store is closed, and its scratch schema dropped, when the test that
//     asked for it ends. A database that cannot be opened, migrated or
//     dropped, or a hook that fails, fails that test.
//   - The fault statements are SQLite's: a PostgreSQL trigger runs a separate
//     trigger function, so the fault tests that use FailTrigger run on SQLite.
//   - Schema leaves out schema_migrations, whose shape the module streams
//     change, and everything that differs between runs or hosts (object ids,
//     SQLite root pages, the database's default collation), so two databases
//     built by the same streams describe as the same string.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite for every backend; PostgreSQL through
//     testcontainers, skipped where Docker cannot run Linux containers).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": module suites run through RunModule, and the PostgreSQL
//     composite runs stay in the composite's packages in app; "Names the
//     migration introduces"; "Dependency rules").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 586 (platform/sql/sqltest: RunModule on each dialect), lines 631
//     to 633 (RunModule runs any module's store suite against every dialect,
//     so a module adds one test file) and line 1044 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/996
//
// # Status
//
// Implemented P2: the PostgreSQL server that the contract runs share, and
// Reset (pull request 3). Staged in P9d: RunModule, OpenSQLite, ScratchDSN,
// Reset over any modules and the fault-test helpers (issue 996), and Schema
// and SchemaGolden (issues 960 and 1098).
//
// # Testing
//
//   - TestAccRunModule, TestAccRunModuleWithoutPostgres,
//     TestAccRunModuleKeyrings and TestAccRunModuleRunsAfterMigrate run
//     RunModule over modules of one table each, and TestAccOpenSQLite and
//     TestAccReset the other two entry points.
//   - TestAccHarnessFailsTheTest provokes each failure in a child run of the
//     test binary and checks that it fails the test with its message.
//   - TestAccPostgresDSN and TestAccScratchDSN run on the shared server and
//     skip with it.
//   - TestUnitFaultStatements, TestUnitErrKeepsTheError and
//     TestAccFaultHelpers cover the fault-test helpers.
//   - The composite store's PostgreSQL runs, TestAccContract and
//     TestAccKeyRotation among them, use PostgresDSN and Reset.
//   - Schema and SchemaGolden are exercised by the composite store's
//     TestAccSchemaGolden on SQLite and PostgreSQL, TestAccMigrationsUpDownUp
//     and TestAccStreamsMigrateAlone, against the schema goldens beside the
//     composite's dialect packages.
package sqltest
