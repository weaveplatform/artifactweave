// Package sqlite opens SQLite databases for the SQL layer: OpenDB returns a
// configured *sql.DB over the pure-Go modernc.org/sqlite driver, and Dialect
// describes the engine to platform/sql/dialect.
//
// # Role
//
// platform layer: the SQLite driver under platform/sql. It opens the pool of
// one SQLite database with the connection settings every store relies on, and
// describes the engine as a dialect.Dialect. It knows nothing of stores,
// migrations or keyrings: the caller pings the pool, migrates it and wraps it
// in a store. Among this module's packages it imports only
// platform/sql/dialect.
//
// # Inputs
//
//   - A database path, or Memory for a private in-memory database, and
//     Options, the busy timeout.
//   - Driving: the store opener, which opens the composite store over the pool
//     that OpenDB returns, and, from STO-08 (issue 996), the module SQL test
//     harness, which opens its databases through OpenDB.
//
// # Outputs
//
//   - OpenDB: a pool over the "sqlite" driver that modernc.org/sqlite
//     registers, not yet connected, or ErrPathRequired for an empty path.
//   - DSN: the connection string OpenDB opens, with its pragmas and
//     IMMEDIATE transactions.
//   - Dialect, SQLite's dialect.Dialect, and IsUniqueViolation, the
//     unique-violation test it carries.
//   - Options, Memory and ErrPathRequired.
//
// # Invariants
//
//   - The driver is modernc.org/sqlite, which is pure Go, so the server builds
//     without CGO on every platform (decision 0004).
//   - Transactions begin IMMEDIATE, taking the write lock up front. With WAL
//     and a busy timeout, 10s unless Options sets one, this serialises writers
//     without deadlocks, and readers keep reading while one writes. Every
//     connection also enforces foreign keys and runs with synchronous NORMAL.
//   - Memory opens a private in-memory database on one connection, which the
//     pool never closes for age or idleness, so the database lives as long as
//     the pool: each connection to ":memory:" sees its own empty database, and
//     a second connection would find none of the tables.
//   - Dialect.Columns lists a table's columns through the table-valued form of
//     PRAGMA table_info, so the table name binds as a parameter.
//   - IsUniqueViolation reports SQLite's unique and primary-key constraint
//     failures only, which the SQL store turns into kernel.ErrConflict; every
//     other constraint failure stays a driver error.
//   - Error texts start with "sqlite:" and stay byte-identical, because
//     operators read them behind the store opener's "open store: ".
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": SQLite through modernc.org/sqlite, CGO-free, with WAL and
//     one writer).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": the platform drivers open the engines, and
//     app/adapters/sql/sqlite opens the composite store over them; "Names the
//     migration introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 132 and 133 (platform/sql/sqlite, the driver open, pragmas and
//     IMMEDIATE transactions, and platform/sql/postgres), line 584 (open and
//     driver settings) and line 1043 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 2, and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/995
//
// # Status
//
// Implemented P2: the SQLite store's open with WAL, the busy timeout and
// IMMEDIATE transactions, the in-memory database on one connection and the
// unique-violation test (pull request 3). Staged in P9d: the migrations left
// for each module's stream, and Dialect gained Columns (issue 994); the driver
// left the composite store's open for this package, with OpenDB and its
// Options (issue 995).
//
// # Testing
//
//   - TestUnitOpenDBRefusesAnEmptyPath, TestUnitDSNCarriesThePragmas,
//     TestAccOpenDBKeepsMemoryOnOneConnection, TestAccOpenDBAppliesThePragmas,
//     TestAccOpenDBBeginsImmediate, TestAccIsUniqueViolation and
//     TestAccIsUniqueViolationReadsTheConstraintCodes run the driver alone,
//     in memory and in temporary files.
//   - The composite store's suites on SQLite open every database through
//     OpenDB: the metadata store's contract (TestAccContractFile and
//     TestAccContractMemory), TestAccKeyRotation, TestAccOpenErrors,
//     TestAccMigrationsUpDownUp, TestAccSchemaGolden and the store opener's
//     TestAccOpenStoreErrors.
package sqlite
