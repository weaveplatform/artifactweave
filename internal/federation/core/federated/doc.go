// Package federated manages federated repositories: their members and the
// configuration sync between them (FED-01), and, as P13 continues, the
// conflict rule, lag and replay.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// Federated repository configurations, whose members are peers paired for
// the federated-repo use case (decision 0022), this instance's base URLs
// (federatedRepoUrlBase, urlBase) and configurations members send.
//
// # Outputs
//
// Members checked and stored in the federation_members table, configSync
// work that sends each member the configuration and retries an unreachable
// one, and the migrate, convertToLocal and member URL operations. Planned:
// converged members, with deploys, deletes, moves, copies
// and property changes synced, conflicts settled by hybrid logical clock time
// with the origin instance as tie-break, full and forced sync, status, lag
// and failed-binary replay (decision 0022; plan P13, task 2).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 337.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Members and configuration sync implemented (FED-01, issue #1930); event
// sync and the rest of P13 to follow.
package federated
