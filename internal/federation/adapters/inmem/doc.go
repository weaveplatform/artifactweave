// Package inmem is the in-memory implementation of the federation module's
// stores: replications and their runs, peers and pairing nonces, federated
// repository members and full syncs, Access Federation targets, platform
// federations and topologies.
//
// # Role
//
// federation module, adapter layer (in-memory): the reference
// implementation of the module's port.Store over a shared memtx.Store. The
// composite in-memory store in app/adapters/inmem builds one Store over its
// own memtx.Store and delegates Replications, Peers, Federation and
// Platform to it. The package imports the standard library, the kernel, the
// platform and the module's core only, and, through the model,
// artifacts/api/repo and retention/api.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Replications, Peers, Federation and Platform,
//     and memtx's Tx, AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (replications, replication_runs, peers, pairing_nonces,
//     federation_members, full_sync_runs, access_federation_targets,
//     platform_federations and topologies), so every call joins the
//     transaction that the caller's context carries, whichever module's
//     store began it. A peer holds its use cases and a topology its sites,
//     which the SQL store keeps in tables of their own.
//   - Deleting a replication deletes its runs in the same call, as the SQL
//     table's cascade does.
//   - Passwords are kept as given: nothing here outlives the process, so
//     there is nothing to seal.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025: docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 346 (the federation adapters) and line 1056 (the metadata store's
//     in-memory store split per owner).
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
// Implemented P2: the replication, peer, federation and platform federation
// stores, as the metadata store's (pull request 3). Staged in P9d: Store and
// New. The services that call them arrive with P11 (replication), P13 (peer
// trust and federated repositories) and P14 (platform federation and
// topology).
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the federation contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
