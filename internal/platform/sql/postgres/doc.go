// Package postgres opens PostgreSQL databases for the SQL layer: OpenDB
// returns a configured *sql.DB over pgx's database/sql adapter, and Dialect
// describes the engine to platform/sql/dialect.
//
// # Role
//
// platform layer: the PostgreSQL driver under platform/sql. It opens the pool
// of one PostgreSQL database, sized and recycled as configured, and describes
// the engine as a dialect.Dialect. It knows nothing of stores, migrations or
// keyrings: the caller pings the pool, migrates it and wraps it in a store.
// Among this module's packages it imports only platform/sql/dialect.
//
// # Inputs
//
//   - A DSN in a form pgx parses, a postgres:// URL or key=value pairs, and
//     Options, the pool size and the connection lifetime.
//   - Driving: the store opener, which opens the composite store over the pool
//     that OpenDB returns, and, from STO-08 (issue 996), the module SQL test
//     harness, which opens its databases through OpenDB.
//
// # Outputs
//
//   - OpenDB: a pool over pgx's database/sql adapter, not yet connected;
//     ErrDSNRequired for an empty DSN, and pgx's parse error behind
//     "postgres: parse dsn: " for one pgx cannot parse.
//   - Dialect, PostgreSQL's dialect.Dialect, and IsUniqueViolation, the
//     unique-violation test it carries.
//   - Options and ErrDSNRequired.
//
// # Invariants
//
//   - The engine is reached through pgx's database/sql adapter, so the SQL
//     layer works on a plain *sql.DB for either engine (decision 0004).
//   - OpenDB does not connect: a DSN that parses but names a server that
//     cannot be reached fails at the caller's first ping or statement.
//   - A zero MaxOpenConns or ConnMaxLifetime keeps the database/sql default:
//     a pool without a size limit whose connections are not recycled for age.
//   - Dialect writes $1 placeholders and locks rows inside transactions with
//     FOR UPDATE.
//   - Dialect.Columns reads the current schema only, the first schema of the
//     search_path and the one an unqualified CREATE TABLE writes to, so a
//     scratch schema selected in the DSN sees only its own tables.
//   - IsUniqueViolation reports SQLSTATE 23505, unique_violation, only, which
//     the SQL store turns into kernel.ErrConflict; every other error stays a
//     driver error.
//   - Error texts start with "postgres:" and stay byte-identical, because
//     operators read them behind the store opener's "open store: ".
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": PostgreSQL through pgx/v5/stdlib).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the contract suites run on PostgreSQL in a container, and skip with a
//     message where Docker is unreachable).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": the platform drivers open the engines, and
//     app/adapters/sql/postgres opens the composite store over them; "Names the
//     migration introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 132 and 133 (platform/sql/sqlite and platform/sql/postgres, the
//     pgx open), line 584 (open and driver settings) and line 1043 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 2, and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/995
//
// # Status
//
// Implemented P2: the PostgreSQL store's open over pgx with the pool settings
// and the unique-violation test (pull request 3). The error texts took a
// lower-case "dsn" with the naming conventions (pull request 882). Staged in
// P9d: the migrations left for each module's stream, and Dialect gained
// Columns (issue 994); the driver left the composite store's open for this
// package, with OpenDB and its Options (issue 995).
//
// # Testing
//
//   - TestUnitOpenDBRefusesAnEmptyDSN, TestUnitOpenDBRejectsAMalformedDSN,
//     TestUnitOpenDBSetsThePool and TestAccIsUniqueViolation run the driver
//     alone, without a server, because OpenDB does not connect.
//   - The composite store's suites on PostgreSQL open every database through
//     OpenDB, on a server that testcontainers starts or
//     ARTIFACTWEAVE_TEST_POSTGRES_DSN names: the metadata store's contract
//     (TestAccContract), TestAccKeyRotation, TestAccOpenErrors (an empty DSN,
//     a malformed one and a closed port), TestAccLegacyHistoryRefused,
//     TestAccStreamsMigrateAlone and TestAccSchemaGolden. The store opener's
//     TestAccOpenStoreErrors opens a malformed DSN through it.
package postgres
