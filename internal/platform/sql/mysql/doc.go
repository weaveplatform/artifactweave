// Package mysql will open MySQL databases for the SQL layer, beside the
// SQLite and PostgreSQL driver packages.
//
// # Role
//
// platform layer: a driver package under platform/sql, which serves one
// database that holds every module's schema (target layout, line 129).
//
// # Inputs
//
// None yet. Planned: the database configuration that selects MySQL.
//
// # Outputs
//
// None yet. Planned: the MySQL counterpart of the sqlite and postgres
// packages (target layout, lines 129-134). Plan P34 adds the MySQL dialect,
// migrations, transactions, query compilation and locking, and runs every
// store contract against a real MySQL.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 129-137.
//   - Plan: docs/implementation/plan.md#p34-database-portability.
//   - Context map: platform has no row
//     (https://github.com/weaveplatform/artifactweave/issues/958).
//   - Decision 0004:
//     docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P34.
package mysql
