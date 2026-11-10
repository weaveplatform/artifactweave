// Package sql will store the evidence module's records in the shared
// database.
//
// # Role
//
// evidence module, adapter layer (SQL). Like every module SQL adapter it is
// named sql after its directory, so an importer that also needs database/sql
// names one of the two.
//
// # Inputs
//
// None yet. Planned: calls through the module's store port, which run in the
// caller's unit of work.
//
// # Outputs
//
// None yet. Planned (target layout, line 966): the store that implements that
// port, with the module's own migration stream in
// migrations/{sqlite,postgres,mysql} (decision 0044, persistence).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 356, and "Adding a new business domain: Evidence (P23)",
//     line 966.
//   - Plan: docs/implementation/plan.md#p23-evidence-and-provenance.
//   - Context map: the evidence row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P23.
package sql
