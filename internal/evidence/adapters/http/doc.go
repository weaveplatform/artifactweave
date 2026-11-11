// Package evidencehttp will serve the evidence endpoints.
//
// # Role
//
// evidence module, adapter layer (HTTP). The package is named evidencehttp so
// that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests through the generated strict server in
// platform/gen/evidence, which make generate creates with the evidence
// specification (target layout, lines 959-960).
//
// # Outputs
//
// None yet. Planned (target layout, line 967): strict_evidence.go, the
// module's part of that API, composed into the app router as a partial strict
// server (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 356, and "Adding a new business domain: Evidence (P23)",
//     lines 959-960 and 967.
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
package evidencehttp
