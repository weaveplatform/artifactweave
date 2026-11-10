// Package accessfed will federate access entities over peers: users, groups,
// permissions and tokens.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// None yet. Planned: Access Federation targets, each a peer paired for
// access-federation, with the entities and the permission and group filters
// chosen for it (decision 0025).
//
// # Outputs
//
// None yet. Planned: one-way streams of user, group, permission and token
// changes from source to target, full broadcasts and server validation
// (decision 0025).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 339.
//   - Plan: docs/implementation/plan.md#p14-platform-federation-and-topology.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P14.
package accessfed
