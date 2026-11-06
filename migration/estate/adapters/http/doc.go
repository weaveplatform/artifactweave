// Package estatehttp will serve the estate endpoints: Mission Control,
// MyJFrog, JFConnect and the bridges.
//
// # Role
//
// estate module, adapter layer (HTTP). The package is named estatehttp so
// that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests to the estate, account and registration
// operations whose contracts plan P40 resolves (task 1).
//
// # Outputs
//
// None yet. Planned: those operations over the estate module, each with a
// verified effect and never a fabricated success (plan P40, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 362.
//   - Plan:
//     docs/implementation/plan.md#p40-estate-and-hosted-service-integrations.
//   - Context map: the estate row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P40.
package estatehttp
