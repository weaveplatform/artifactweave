// Package topology will hold platform federations and the grid topology:
// which instances form the platform, and how healthy they are.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// None yet. Planned: pfed federations with their connection groups, and the
// peer records whose last signed exchange gives each instance its status
// (decision 0025).
//
// # Outputs
//
// None yet. Planned (target layout, line 340): the pfed and grid views of the
// topology and its health (plan P14, task 2).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 340.
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
package topology
