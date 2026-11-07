// Package dialect is the SQL layer that every SQL store is built on: the
// Dialect that describes what differs between engines, the unit of work that
// a context carries over one *sql.DB, the migrator, Store, the handle whose
// helpers run statements, encode columns, page lists and seal secret
// columns, and the registration through which key rotation reaches every
// module's sealed columns.
//
// # Role
//
// platform layer: the SQL dialect, unit of work, migrator, sealing helpers and
// reseal engine that every module SQL adapter uses. One unit of work spans
// every module store (decision 0044, "Persistence"), so the platform owns it,
// with the dialect layer and the migrator. The SQLite and PostgreSQL drivers
// each declare a Dialect with their embedded migrations; the module SQL
// adapters are built over one Store, call its helpers and describe the
// columns they seal in a Module each. The reseal engine sits beside the
// keyring because re-sealing opens and seals values, and the keyring never
// leaves the package. The package imports only the kernel, for its sentinels
// and paging, and platform/crypt, for the keyring.
//
// # Inputs
//
//   - An open *sql.DB and the engine's Dialect: its placeholder style, its
//     row-lock suffix, its migrations and its test for a unique violation.
//   - A *crypt.Keyring for the sealed columns, or nil.
//   - Each module's registration, in the canonical module order: the sealed
//     columns its store writes, and a hook to run before them.
//   - Statements written with "?" placeholders, which Rebind converts to $1
//     style for PostgreSQL.
//   - Migration files named NNNN_name.sql, with "-- +up" and "-- +down"
//     sections.
//
// # Outputs
//
//   - Store and New. The unit of work: Tx, AfterCommit and InTx. Statements:
//     Q, Exec, ExecOne, InsertID, Exists, Rows, Wrap and LockClause. Sealed
//     columns: Seal and Unseal. And DB, Dialect and Close.
//   - Module registration and key rotation: SealedColumn, FirstKey, Module
//     and Reseal, which re-seals the columns the modules register under the
//     keyring's active version.
//   - Statement text: Upsert, InsertIgnore, Insert and Placeholders. Column
//     values: Micros, FromMicros, Millis, FromMillis, ToJSON, MustJSON,
//     FromJSON, RawJSON, FromRaw, NullString and EntBytes. Keyset paging:
//     Keyset, OrderBy, Itoa and LimitOr. Scanning: EntRow, PeersScanner and
//     OpsCols.
//   - The migrator: Migration, LoadMigrations, Migrate, Rollback, Version and
//     MustSub.
//   - ErrTransaction, ErrMigration and ErrNoKeyring.
//
// # Invariants
//
//   - The unit of work is keyed to one *sql.DB. A context carries at most one
//     transaction, and only stores over its pool join it, so every module
//     store is built over the same Store: a store over another pool writes
//     outside the transaction and does not roll back with it.
//   - A nested Tx joins the outer transaction, and an error its function
//     returns fails the outer transaction even when the outer function drops
//     it. platform/tx/memtx commits in that case, as tx.Runner records, so
//     callers return inner errors.
//   - AfterCommit runs its functions in order once the transaction has
//     committed, never after a rollback or a failed commit, and at once
//     outside a transaction.
//   - The keyring never leaves the package: stores seal and open values only
//     through Seal and Unseal, which bind each value to its column's purpose
//     and its row (decision 0017), and Reseal re-seals them under the same
//     binding, which it takes from the column's registration.
//   - Every sealed column belongs to exactly one module registration. Reseal
//     reaches only registered columns, so the values of a column left out
//     stay under a retired key and no longer open once that key leaves the
//     configuration. Reseal takes the modules in the canonical module order
//     (decision 0044, "Persistence"), and each module's hook before its
//     columns. TestAccSealedColumnsComplete matches every Seal call in the
//     repository against the registrations, and fails on a purpose
//     registered twice.
//   - A store that embeds *Store gets its methods promoted, and a method the
//     store declares under the same name hides the promoted one without a
//     compile error. So Store exports no method named like an accessor of the
//     metadata store's aggregate (Keys, Items, Repos, ...) other than Tx,
//     AfterCommit and Close, or like a method an embedding store declares,
//     such as Reseal, which is why the layer's Reseal is a function.
//     TestUnitDialectStoreShadowsNoAccessor checks it.
//   - Every SQL adapter keeps the schema conventions of research 15, section
//     4. Times are BIGINT Unix microseconds, NULL for a zero time (Micros),
//     and durations BIGINT milliseconds (Millis): integers compare and sort
//     alike on every engine, which keeps keyset paging exact. Documents are
//     TEXT (ToJSON), so they round-trip byte for byte, and empty values are
//     NULL (NullString, RawJSON, EntBytes). A sealed value has a BLOB (BYTEA)
//     column of its own, never a place inside a document (Seal). Text columns
//     that lists sort by are byte-ordered (COLLATE "C" on PostgreSQL), and a
//     cursor binds the parts of integer columns as integers (Keyset), so a
//     cursor means the same position on every store, the in-memory ones
//     included.
//   - The schema_migrations table, its columns and the statements the
//     migrator runs on it, and the SQL text the helpers render, are persisted
//     or observed by the engines. Error and panic texts reach operators and
//     logs, the "sqlcommon:" prefix of the sentinels and panics included. All
//     of them stay byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": SQL stores over SQLite and PostgreSQL through one dialect
//     layer, with embedded migrations per dialect).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (sealed columns, with the purpose and the row identity as additional
//     authenticated data).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one unit of work for every module store, one *sql.DB
//     with platform/sql/dialect; "Names the migration introduces").
//   - Research 15: docs/research/15-data-model.md, section 4 (the derived
//     storage model and its column conventions).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 130 and 131 (platform/sql/dialect), line 483 and line 583 (its
//     files), line 579 (the platform owns the dialect layer, the unit of work
//     and the migrator), line 917 (Tx opens the unit of work through it),
//     line 1042 (the mapping row) and lines 1091 to 1094 (the unit of work
//     is platform-level and context-carried).
//   - Plan: docs/implementation/plan.md#p2-stores, task 2, and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/992
//   - https://github.com/weaveplatform/artifactweave/issues/993
//
// # Status
//
// Implemented P2: the dialects, the unit of work, the migrator, the store
// handle and its helpers, sealed columns included (pull request 3). In the
// v0.1.0 release InsertIgnore gained the form without a conflict target, for
// the property tables, whose unique indexes differ between the dialects
// (issue 338, pull request 339). Staged in P9d: the scanner helpers were
// gathered into one file (issue 959, pull request 1122), and the layer leaves
// the SQL stores' shared package for this one, exported for the module SQL
// adapters (issue 992). The reseal engine joins the keyring here, and each
// module registers its sealed columns (issue 993).
//
// # Testing
//
//   - The metadata store's contract suite on SQLite and PostgreSQL
//     (TestAccContractFile, TestAccContractMemory and TestAccContract), the
//     SQL stores' fault tests, TestAccKeyRotation and TestAccMigrationsUpDownUp
//     run every helper through the SQL stores.
//   - TestAccTxDroppedInnerErrorFailsOuter covers the nested case that the
//     contract suite leaves out, and TestAccTxJoinsTheTransactionOfItsPool
//     the pool the unit of work is keyed to.
//   - TestAccMigratorStopsAtAFailingStep,
//     TestAccMigratorRecordsEachStepInItsTransaction and
//     TestAccMigratorReportsAnUnreadableVersionTable run the migrator on
//     SQLite, through modernc.org/sqlite, which the test package registers.
//   - TestUnitLoadMigrationsReadsSections and
//     TestUnitLoadMigrationsRejectsMalformedFiles parse migrations from file
//     systems in memory, and TestUnitMustSubPanicsOnInvalidDirectory checks
//     MustSub's refusal.
//   - TestUnitTxReportsCommitFailure, TestUnitMigratorReportsDriverFaults,
//     TestUnitMigratorReportsUnreadableMigrations and
//     TestUnitStoreReportsDriverFaults use a database/sql driver whose named
//     steps fail, for the failures SQLite cannot produce on demand.
//   - TestUnitResealNeedsAKeyring, TestAccResealFollowsTheModuleOrder,
//     TestAccResealStopsAtTheFirstError,
//     TestUnitResealReportsAnUnscannableRow,
//     TestAccResealKeepsAValueChangedSinceItsRead and
//     TestAccResealReadsByteKeysAsText run Reseal over tables of their own,
//     for the order, the failures and the concurrent write that
//     TestAccKeyRotation leaves out.
//   - TestUnitSealReportsTheKeyringsRefusal,
//     TestUnitUpsertOfKeyColumnsOnlyDoesNothing and
//     TestAccMustJSONPanicsOnUnencodable cover the rest, and a test file
//     checks at compile time that *Store implements tx.Runner.
package dialect
