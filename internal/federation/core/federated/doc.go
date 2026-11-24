// Package federated manages federated repositories: their members, the
// configuration sync between them (FED-01), and the repo:<key> stream kind
// that converges their content under the conflict rule (FED-02); as P13
// continues, full sync, lag and replay.
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
// Planned: full and forced sync, status, lag and failed-binary replay
// (decision 0022; plan P13, FED-04 to FED-06).
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
// propagation implemented (FED-02, issue #1931); the rest of P13 to follow.
package federated
