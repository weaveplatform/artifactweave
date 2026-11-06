// Package project will manage projects: membership, quotas and environments,
// and attaching repositories to projects.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// None yet. Planned: project, membership, quota and environment requests,
// through the Projects, Environments and Membership contracts of access/api
// (target layout, lines 267 and 546).
//
// # Outputs
//
// None yet. Planned (target layout, line 546): project.go, membership.go,
// quota.go and environments.go. A project attach calls
// artifacts/api.Repositories.SetProject, so attach runs through api packages
// in both directions and forms no package cycle (lines 571-573).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 285, and "Access and identity", lines 546 and 571-573.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0013:
//     docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11.
package project
