// Package api will be the retention module's published interface: cleanup
// policies, archive policies and restores.
//
// # Role
//
// retention module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
// None yet. Planned (target layout, line 327): retention.go with the
// CleanupPolicies, ArchivePolicies and Restores contracts.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 327.
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
package api
