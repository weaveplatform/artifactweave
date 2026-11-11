// Package cleanup will run package cleanup policies: scheduling, dry runs,
// run reports, enablement and force-disable.
//
// # Role
//
// retention module, core layer.
//
// # Inputs
//
// None yet. Planned: cleanup policies, created disabled and enabled through
// /enablement, whose version criteria are evaluated over the package index
// with the package type's comparator, never by path globbing (decision 0010).
//
// # Outputs
//
// None yet. Planned: runs recorded with a trigger id, with their status and
// their reports (decision 0010; plan P11, task 3).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 328.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - Context map: the retention row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11.
package cleanup
