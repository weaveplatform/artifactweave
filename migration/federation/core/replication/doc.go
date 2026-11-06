// Package replication will run repository replication: single push and pull
// first, then multi-push and event replication.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// None yet. Planned: replication configurations and their execute requests
// (plan P11, task 4).
//
// # Outputs
//
// None yet. Planned (target layout, line 338): single push and pull between
// two instances in P11, then multi-push, event-triggered and checksum
// replication with per-target status and retry in P37.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 338.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p37-advanced-replication-and-interoperability.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11, P37.
package replication
