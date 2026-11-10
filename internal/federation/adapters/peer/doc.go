// Package peer will serve and call artifactweave's own peer API,
// /artifactweave/peer/v1.
//
// # Role
//
// federation module, adapter layer (peer protocol).
//
// # Inputs
//
// None yet. Planned: signed peer requests through the generated server in
// platform/gen/peer, which make generate creates with the peer specification
// (target layout, lines 344 and 816).
//
// # Outputs
//
// None yet. Planned: the server side of the peer API, and the calls to peers
// through the generated client pkg/client/peer (target layout, line 816).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 344, and "Generated code and API specifications", line 816.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//     Task 0 specifies the peer API.
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
