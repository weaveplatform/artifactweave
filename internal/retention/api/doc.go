// Package api will be the retention module's published interface: cleanup
// policies, archive policies and restores. Today it holds RunStatus, the
// status of a cleanup or smart-archiving run.
//
// # Role
//
// retention module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules). RunStatus is
// declared here, not in the module's core/model, because federation's
// full-sync runs report the same statuses, and a module imports another
// only through that module's api.
//
// # Inputs
//
// None. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
//   - RunStatus with its ten Run* statuses and Terminal. The retention
//     model's CleanupRun and the federation model's FullSyncRun report it.
//
// Planned (target layout, line 327): retention.go with the CleanupPolicies,
// ArchivePolicies and Restores contracts.
//
// # Invariants
//
//   - The statuses are JFrog's strings and are stored, so they never
//     change. Terminal is true only for COMPLETED, COMPLETED_WITH_ERRORS,
//     STOPPED and FAILED: a run in any other status may still change.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 327.
//   - Plan:
//     docs/implementation/plan.md#p2-stores, task 0,
//     #p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - Context map: the retention row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" and "Names the migration introduces", which lists
//     RunStatus.
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: RunStatus among the shared domain types (pull request 3).
// Moved here in P9d. The contracts arrive with P11 and P16.
//
// # Testing
//
// The retention model's TestUnitRunStatusTerminal checks which statuses are
// final, and the metadata store's CleanupRuns and FullSyncs contract
// subtests store and read them.
package api
