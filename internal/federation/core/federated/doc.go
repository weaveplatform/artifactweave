// Package federated will manage federated repositories: members, the conflict
// rule, lag and replay.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// None yet. Planned: federated repository configurations, whose members are
// peers paired for the federated-repo use case (decision 0022).
//
// # Outputs
//
// None yet. Planned: converged members, with deploys, deletes, moves, copies
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
// Placeholder: implemented by P13.
package federated
