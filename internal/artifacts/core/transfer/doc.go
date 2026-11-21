// Package transfer publishes current-authorized local item snapshots and
// immutable content to cross-module replication orchestration.
//
// # Role
//
// Artifacts core owns source identity and content reads. Federation consumes
// artifacts/api.TransferSource instead of importing item stores or blob layout.
//
// # Inputs
//
// Source receives the shared metadata Store, immutable blob store and current
// project/credential callbacks bound by the artifacts module. Callers supply
// repository identities, bounded pages and repository-relative ANT selections.
//
// # Outputs
//
// Source implements artifacts/api.TransferSource: current-authorized item
// snapshots, immutable owned bodies and transactional checkpoint callbacks.
// Failed pages disclose no partial metadata. Opening a body refuses a metadata
// transaction; current-version settlement and exact absence remain local work.
//
// # References
//
//   - P11 REP-02: https://github.com/weaveplatform/artifactweave/issues/1761.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//
// # Status
//
// P11 source boundary in implementation. Transport, durable progress and
// replication orchestration remain owned by federation.
package transfer
