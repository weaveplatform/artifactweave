// Package removal publishes guarded indexed-file deletion for retention.
//
// # Role
//
// Artifacts module, core layer. The module binds its current credential and
// permission evaluator; retention owns policy selection and execution state.
//
// # Inputs
//
// The shared metadata store, normal item service, original credential reference
// and one indexed-file snapshot. Revalidate checks current selection facts;
// Record acknowledges actual effects and fences run/lease state. Callbacks
// perform local metadata operations in the deletion's transaction.
//
// # Outputs
//
// New binds artifacts/api.ItemRemoval. Remove returns actual removed file
// identities and counts, including companions, after both callbacks succeed.
// Any failure rolls back item, trash, index, events and acknowledgement together.
// No operation collects physical blobs or interprets a cleanup policy.
//
// # Invariants
//
// Native Delete applies to both the selected file and each format-owned sidecar.
// Exact current item facts fence overwrites, property changes and downloads;
// an indexed file can never authorize recursive folder deletion. The normal
// service retains ownership of trash, package hooks, references and events.
//
// # References
//
//   - docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1758.
//
// # Status
//
// Implemented guarded removal boundary for CLN-04. Retention owns durable run
// admission, worker traversal, stop and recovery; CLN-05 owns report wire views.
//
// # Testing
//
// removal_test.go exercises normal trash/restore and bypass, package pruning,
// events, acknowledgement rollback, stale facts and sidecar native permissions.
package removal
