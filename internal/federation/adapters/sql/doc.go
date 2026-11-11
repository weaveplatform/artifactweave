// Package sql is the SQL implementation of the federation module's stores:
// replications with their sealed passwords and their runs, peers and
// pairing nonces, federated repository members and full syncs, Access
// Federation targets, platform federations and topologies, with the
// module's registration with the SQL layer.
//
// # Role
//
// federation module, adapter layer (SQL). Store implements the module's
// port.Store (port.ReplicationStore, port.PeerStore, port.FederationStore
// and port.PlatformStore over the shared unit of work) over a shared
// dialect.Store, on SQLite and PostgreSQL alike. Module registers the
// federation stream (adapters/sql/migrations) and the sealed column
// replications.password, sixth in the canonical module order. The composite
// SQL store in app/adapters/sql builds one Store over its own dialect
// store, delegates Replications, Peers, Federation and Platform to it and
// names Module in its module list. The package imports the standard
// library, the kernel, the platform, the module's core and its stream only,
// and retention/api, whose run statuses a full sync records; through the
// model, artifacts/api/repo.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the federation
//     stream's tables.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Replications, Peers, Federation and Platform,
//     and the dialect store's Tx, AfterCommit and Close.
//   - Module: the name federation, the stream migrations.FS and the sealed
//     column replications.password.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence"). That is what
//     lets a federation change and the outbox entry that records it commit
//     together (decision 0021).
//   - Every column the store seals is in Module: replications.password,
//     with the purpose "replications.password" and the replication's key as
//     the row id, so key rotation re-seals it (decision 0017). Neither ever
//     changes: both are authenticated with every value.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025: docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order;
//     "Names the migration introduces", which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 346 (the federation adapters), line 619 (the federation SQL
//     adapter) and lines 631 to 633 (RunModule).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1059
//
// # Status
//
// Implemented P2: the replication store with its sealed passwords, and the
// peer, federation and platform federation stores, as the metadata store's
// (pull request 3). Moved here in P9d, which added Store, New and Module. The
// services that call them arrive with P11 (replication), P13 (peer trust and
// federated repositories) and P14 (platform federation and topology).
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the federation contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - replication_test.go: TestAccReplicationsFaults (a fault injected after
//     a successful statement fails the write), TestAccReplicationsSealed (a
//     sealed password refuses a read without the keyring, and one written
//     without a keyring reads back) and TestAccReplicationsUnmigrated (every
//     call reports the driver's error on a database without the schema).
package sql
