// Package federation will be the federation module: peer trust, outbox
// delivery, federated repositories, replication, Access federation and
// topology.
//
// # Role
//
// federation module, module root. Only app imports a module root; other
// modules reach federation through federation/api (decision 0044, dependency
// rules). Its storage skeleton (the module's ports, models and store
// adapters) sits in subpackages ahead of its phase; skeleton modules get no
// constructor until then (decision 0044, composition and HTTP).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New, among them the outbox
// that the sync sender reads as one consumer per peer (target layout, lines
// 736-739).
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps). The module's layers are its
// subpackages: api, core/peer, core/pairing, core/sync, core/federated,
// core/replication, core/accessfed, core/topology, adapters/peer and
// adapters/http.
//
// # References
//
//   - Context map, federation row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns peer trust, outbox delivery, federated repositories, replication,
//     Access federation and topology; phases P11, P13, P14 and P37.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 331-346, and "Webhooks and events (P11, P13)",
//     lines 717-744.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology,
//     #p37-advanced-replication-and-interoperability.
//
// # Status
//
// Placeholder: implemented by P11, P13, P14, P37.
package federation
