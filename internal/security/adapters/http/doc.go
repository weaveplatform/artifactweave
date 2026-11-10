// Package securityhttp will serve the security module's REST endpoints.
//
// # Role
//
// security module, adapter layer (HTTP). The package is named securityhttp so
// that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests through the generated strict servers of the
// xray, catalog and curation specifications (target layout, line 817).
//
// # Outputs
//
// None yet. Planned: the module's parts of those APIs, composed into the app
// router as partial strict servers (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 359, and "Generated code and API specifications", line 817.
//   - Plan:
//     docs/implementation/plan.md#p26-security-scanning-and-intelligence,
//     #p27-advanced-security-and-curation.
//   - Context map: the security row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P26, P27.
package securityhttp
