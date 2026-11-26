// Package federated manages federated repositories: their members, the
// configuration sync between them (FED-01), and the repo:<key> stream kind
// that converges their content under the conflict rule (FED-02), with its
// full sync and the member operations (FED-04), and failed-binary controls
// (FED-06); as P13 continues, lag.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// Federated repository configurations, whose members are peers paired for
// the federated-repo use case (decision 0022), this instance's base URLs
// (federatedRepoUrlBase, urlBase) and configurations members send.
//
// # Outputs
//
// Members checked and stored in the federation_members table, configSync
// work that sends each member the configuration and retries an unreachable
// one, and the migrate, convertToLocal and member URL operations.
//
// Stream, the repo:<key> stream kind of the sync engine: as the item feed it
// appends each change to a federated repository with members (a deploy,
// copy or move landing, a property change, a delete or a move's source) to
// the repository's stream in the change's transaction, as the item's whole
// state, and records the change's stamp; as the applier it settles another
// member's entry by the conflict rule (later hybrid logical clock time
// wins, the higher origin instance id breaks a tie, a tombstone stops an
// older change) and applies the winner through the artifacts module's
// FederatedItems, keeping a content change whose blob has not arrived
// pending until it does. It pauses sending to a disabled member, or to
// every member while this instance's own is disabled, and prunes
// tombstones older than the outbox retention window. The stamps live in
// federation_item_stamps (ItemStampStore).
//
// The repository's snapshot for full sync (SnapshotItems, LocalItems,
// Reconcile, Discard) and Operations' fullSync and forceFullSync, which ask
// each member to resynchronise from this instance through the work queue
// (FullSyncRequestKind, Resyncer, Membership.QueueFullSync); recovery and
// reconnect, removeMember, prioritization and propagateCreatedTime, kept
// in instance settings. A member dropped from a repository is forgotten by
// the sync engine (Membership.Dropped).
//
// Binaries lists, immediately replays and deletes failed content fetches,
// with repository task counts for administrators and a Read-authorized
// repoState projection. Replay never performs network I/O in a metadata
// transaction and never publishes a superseded change or an operator-cancelled
// task. A normal full sync repairs same-stamp pending content too.
//
// Planned: status and lag (decision 0022; plan P13, FED-05).
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
// Members and configuration sync implemented (FED-01, issue #1930); change
// propagation implemented (FED-02, issue #1931); full sync and the member
// operations implemented (FED-04, issue #1932); failed binaries implemented
// (FED-06, issue #1935); the rest of P13 to follow.
package federated
