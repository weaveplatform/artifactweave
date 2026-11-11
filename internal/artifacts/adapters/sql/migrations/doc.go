// Package migrations holds the artifacts module's migration stream: the SQLite
// and PostgreSQL schema of the tables the module's SQL adapter owns.
//
// # Role
//
// artifacts module, adapter layer (SQL): the module's stream, second in the
// canonical module order, so the migrator in platform/sql/dialect applies it
// after the streams before it and records its migrations under the name
// artifacts in schema_migrations (decision 0044, "Persistence").
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
//   - The stream owns 28 tables, which no other stream creates or references:
//     blobs, repositories, repository_secrets, repository_members,
//     repository_keypairs, repository_header_secrets, items, item_properties,
//     item_stats, trash, packages, package_versions, package_version_files,
//     blob_replicas, cold_blobs, cold_blob_replicas, archived_items, builds,
//     build_modules, build_properties, build_artifacts, build_dependencies,
//     build_promotions, build_retention, remote_sources, remote_cache,
//     gitlfs_locks and bower_registrations.
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
//     first; no foreign key crosses a module boundary) and lines 251, 296 and
//     606 (migrations/{sqlite,postgres} under adapters/sql).
//   - https://github.com/weaveplatform/artifactweave/issues/994
//
// # Status
//
// Implemented P9d: the baseline (issue 994).
//
// # Testing
//
// The composite store's tests on SQLite and PostgreSQL run every stream:
// TestAccSchemaGolden compares a fresh database with the goldens,
// TestAccMigrationsUpDownUp rolls every stream back and applies it again on
// SQLite, and TestAccStreamsMigrateAlone migrates each stream up and down alone
// in an empty PostgreSQL schema.
package migrations
