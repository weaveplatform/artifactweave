// Package migrations holds the platform stream: the SQLite and PostgreSQL
// schema of the tables of the capabilities every module shares, which the
// platform's SQL adapters own.
//
// # Role
//
// platform layer (SQL store): the platform stream, first in the canonical
// module order, so the migrator in platform/sql/dialect applies it before every
// module stream and records its migrations under the name platform in
// schema_migrations (decision 0044, "Persistence"). It sits under
// platform/store/sql because the target document's platform/sql is the dialect
// layer.
//
// # Inputs
//
// None: the migration files are embedded at build time.
//
// # Outputs
//
//   - FS: a directory per dialect, sqlite and postgres, named like the dialect,
//     of NNNN_name.sql files with "-- +up" and "-- +down" sections. The stream
//     starts at 0001_baseline.sql.
//
// # Invariants
//
//   - The stream's versions only grow. A schema change is a new file with the
//     next version: a database records the versions it has applied and never
//     runs one again, so a change to an applied file never reaches it.
//   - The baselines of the eight streams, applied in the canonical module
//     order, reproduce the committed schema goldens byte for byte
//     (TestAccSchemaGolden, on SQLite and PostgreSQL).
//   - No foreign key leaves the stream (decision 0044, "No cross-module foreign
//     keys"), so the stream migrates up and down on its own.
//   - The stream owns 15 tables, which no other stream creates or references:
//     settings, leases, work_items, config_documents, config_secrets, instance,
//     outbox_sequences, outbox, peer_cursors, applied_markers,
//     failed_blob_fetches, uploads, api_replays, request_budget_guard and
//     request_budgets.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": per-module migration streams in the canonical module
//     order, no cross-module foreign keys).
//   - Decision 0004:
//     docs/research/decisions/0004-storage-sql-metadata-and-blobs.md (embedded
//     migrations per dialect).
//   - Research 15: docs/research/15-data-model.md, section 4 (the storage
//     model).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 624 to 633 (one stream per module, recorded by module, platform
//     first; no foreign key crosses a module boundary) and line 588 (the
//     platform stream).
//   - https://github.com/weaveplatform/artifactweave/issues/994
//
// # Status
//
// Implemented P9d: the baseline (issue 994).
//
// # Testing
//
// The SQLite and PostgreSQL drivers' tests run every stream:
// TestAccSchemaGolden compares a fresh database with the goldens,
// TestAccMigrationsUpDownUp rolls every stream back and applies it again on
// SQLite, and TestAccStreamsMigrateAlone migrates each stream up and down alone
// in an empty PostgreSQL schema.
package migrations
