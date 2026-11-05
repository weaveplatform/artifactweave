// Package sync will deliver outbox events to peers: the sender,
// acknowledgements, idempotent apply and the full-sync fallback.
//
// # Role
//
// federation module, core layer. It shares its name with the standard
// library's sync, so a file that needs both imports one under another name.
//
// # Inputs
//
// None yet. Planned: the outbox, read as the consumer federation:<peer>, and
// the acknowledgements peers return (target layout, lines 736-738).
//
// # Outputs
//
// None yet. Planned (target layout, lines 737-739): events signed and sent to
// each peer, cursors advanced by acknowledgements, and peer events applied
// idempotently through artifacts/api.Items.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 336, and "Webhooks and events (P11, P13)", lines 736-739.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P13.
package sync
