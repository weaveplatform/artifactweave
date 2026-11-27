// Package api publishes federation's cross-module cleanup observation contract.
// Other peer, repository, replication and topology contracts remain planned.
//
// # Role
//
// federation module, api layer. It may import only kernel, platform/outbox
// and its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// Bounded current-scope and version identities. Callers are other modules and
// the gateways; no adapter or owner implementation is imported.
//
// # Outputs
//
// Cleanup exposes bounded observations and local publication fences. Other
// planned contracts (target layout, line 333) are federation.go with the Peers,
// FederatedRepos, Replications and Topology contracts, and events.go.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 333.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology,
//     #p37-advanced-replication-and-interoperability.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Cleanup is implemented by P13 FED-07. Other contracts remain with P13/P14/P37.
package api
