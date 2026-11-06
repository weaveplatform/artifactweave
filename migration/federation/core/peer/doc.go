// Package peer will hold peer identities and keys, and sign and verify the
// requests that peers exchange.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// None yet. Planned: this instance's identity and Ed25519 key pair, whose
// private key is a sealed column, and the peer records that pairing creates
// (decision 0021).
//
// # Outputs
//
// None yet. Planned: signed requests, and verified peers with their instance
// id, base URL, public key, protocol version, granted use cases, status and
// last-seen time (decision 0021).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 335.
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
package peer
