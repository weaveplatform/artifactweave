// Package peers will run multi-instance scenarios: several artifactweave
// instances that pair, federate and converge.
//
// # Role
//
// Test support, outside the modules. The acceptance-peers job runs it on
// Linux (plan P13, task 3).
//
// # Inputs
//
// None yet. Planned: several running instances and the peer protocol between
// them.
//
// # Outputs
//
// None yet. Planned: scenario results, such as three instances converging
// after deploys, deletes and property changes on each, and full sync
// repairing a partitioned member (plan P13, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Repository
//     root", line 63, and "Tests and enforcement", line 852.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//   - Context map: the federation row, restated in the federation module
//     root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P13.
package peers
