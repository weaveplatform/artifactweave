// Package sql is the SQL implementation of the stores the platform owns:
// the persistent work queue, leases, settings, configuration documents and
// this instance's identity, with the platform's registration with the SQL
// layer.
//
// # Role
//
// platform layer, adapter. Store implements the platform's ports
// (work.WorkQueue, work.LeaseStore, settings.SettingsStore,
// configdoc.ConfigStore and instance.InstanceStore) over a shared
// dialect.Store, on SQLite and PostgreSQL alike. Module registers the
// platform stream (platform/store/sql/migrations) and the platform's sealed
// columns, first in the canonical module order. The metadata store's SQL
// store builds one Store over its own dialect store, delegates its five
// platform accessors to it, registers the layout fence as a watch and names
// Module first in its module list; STO-19 moves that composition into app.
// The package imports the standard library, the kernel and platform
// packages only: the ports, the dialect layer and the stream.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the platform
//     stream's tables, and the configdoc.Watch values its config store
//     calls.
//   - The ports' calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Settings, Leases, WorkQueue, Configs and
//     Instance, and the dialect store's Tx, AfterCommit and Close.
//   - Module: the name platform, the stream migrations.FS and the sealed
//     columns config_secrets.value and instance.private_key.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence").
//   - The config store runs the watches of a document's kind inside the
//     write's transaction, in the order given: after a Put that creates the
//     document or changes its Document bytes, and after every successful
//     Delete. A watch's error rolls the write back. A Put reads the document
//     it replaces only when a watch is registered for the kind.
//   - Every column the stores seal is in Module, with the purpose and row id
//     its store seals with, so key rotation re-seals all of them (decision
//     0017). Neither ever changes: both are authenticated with every value.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the platform owned them.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (configuration documents, the persisted form of the descriptor).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     (the instance identity).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": the platform's adapters in platform/store; "Names the
//     migration introduces", which lists Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 582 to 591 (platform/sql with the platform stream, and
//     platform/work).
//   - Plan: docs/implementation/plan.md#p2-stores, #p7-npm-go-maven-locals
//     and #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1056
//
// # Status
//
// Implemented P2: the settings, lease, configuration document and instance
// stores, with their sealed secrets and key, as the metadata store's (pull
// request 3). P7: the persistent work queue (issue 297, pull request 299).
// P9: layout writes fence the remote repositories that use the layout (pull
// request 680). Staged in P9d: Store, New and Module, with the watches in
// place of the layout fence.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the platform contract
//     (platform/store/storetest.Run, the watch cases included) on SQLite in
//     memory and in a file through sqltest.RunModule. The metadata store's
//     contract runs the same cases but the watch cases on PostgreSQL.
//   - work_test.go: the work queue's driver failures, on a closed database
//     and with a trigger that fails every update.
//   - operations_test.go: TestAccPlatformFaults (a fault injected after a
//     successful statement fails the settings, lease and config write or
//     read), TestAccPlatformSealed (the config secrets refuse a read
//     without the keyring) and TestAccPlatformUnmigrated (every settings,
//     lease and config call reports the driver's error on a database
//     without the schema).
package sql
