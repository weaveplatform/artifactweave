// Package retentionhttp will serve the retention endpoints: cleanup and
// archive policies, their runs, and restores.
//
// # Role
//
// retention module, adapter layer (HTTP). The package is named retentionhttp
// so that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests to the cleanup and archive operations of the
// Artifactory specification, through the generated strict server in
// platform/gen/artifactory (target layout, line 810).
//
// # Outputs
//
// None yet. Planned: the module's part of that API, composed into the app
// router as a partial strict server (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 329, and "Generated code and API specifications", line 810.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention.
//   - Context map: the retention row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11, P16.
package retentionhttp
