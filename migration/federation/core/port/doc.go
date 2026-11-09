// Package port declares the federation module's store ports: replications,
// peers and pairing nonces, federated repository membership and full syncs,
// the platform federation records, and Store, the part of the metadata
// store the module uses.
//
// # Role
//
// federation module, core layer, store ports. The metadata store's SQL and
// in-memory stores implement ReplicationStore, PeerStore, FederationStore
// and PlatformStore, and the metadata store satisfies Store; STO-15 moves
// those implementations into the module's adapters. The ports take and
// return the module's core/model values and kernel.Page, and join the unit
// of work of platform/tx: an outbox entry is written in the transaction of
// the change it records (decision 0021), so these stores run inside the
// shared unit of work rather than one of their own. The outbox itself, the
// peers' cursors and the instance identity are platform ports
// (platform/outbox and platform/instance). The metadata store aliases every
// name here (its bridge_federation.go) until PRO-02 deletes the bridge.
//
// # Inputs
//
//   - Replications and their runs, peers, pairing nonces, members, full-sync
//     runs, Access Federation targets, platform federations and topologies to
//     store; keys, ids and instance ids to look up; a PeerFilter or a
//     kernel.Page to narrow a listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - ReplicationStore: Put, Get, ForRepo, List, Delete and DeleteForRepo;
//     PutRun and LastRun.
//   - PeerStore: Put, Get, List and Delete; AddNonce, RedeemNonce and
//     PruneNonces.
//   - FederationStore: SetMembers, Members and MembersWithPeer;
//     CreateFullSync, FullSync, UpdateFullSync and FullSyncs.
//   - PlatformStore: PutAccessTarget, AccessTarget, AccessTargets and
//     DeleteAccessTarget; PutFederation, Federation, Federations and
//     DeleteFederation; PutTopology, Topology, Topologies and DeleteTopology.
//   - PeerFilter.
//   - Store: tx.Runner, Replications, Peers, Federation and Platform.
//
// # Invariants
//
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrNotFound for a
//     missing record; kernel.ErrConflict for a nonce hash that exists, two
//     members that name one peer or two sites that share a name; and
//     kernel.ErrInvalid for a replication without a key or a member not yet
//     resolved to a peer.
//   - RedeemNonce redeems a nonce once: an unknown or expired nonce is
//     kernel.ErrNotFound and a redeemed one kernel.ErrConflict, so a pairing
//     token completes one pairing.
//   - A replication's password is sealed at rest (decision 0017).
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025: docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence", "Type-alias bridges" and "Names the migration
//     introduces", which lists Store).
//   - Research 15: docs/research/15-data-model.md, lines 689 and 690, 701 and
//     702, 707 and 708, and 719 to 721 (the tables behind these ports).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton holds core/port/store.go and its
//     store adapters), lines 341 and 342 (the federation module's
//     core/port), lines 577 and 578 (each module owns its store ports) and
//     line 1055 (the metadata store split into each module's
//     core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1,
//     #p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: ReplicationStore, PeerStore, FederationStore and
// PlatformStore, with the metadata store's in-memory, SQLite and PostgreSQL
// stores (pull request 3). Staged in P9d, where Store is declared. The
// services that call the ports arrive with P11 (replication), P13 (peer
// trust and federated repositories) and P14 (platform federation and
// topology).
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// metadata store's Replications, Peers, PairingNonces, FederationMembers,
// FullSyncs, AccessTargets, PlatformFederations and Topologies contract
// subtests run the ports on its in-memory, SQLite and PostgreSQL stores,
// and compile-time assertions in the in-memory and SQL stores' tests show
// that both satisfy Store.
package port
