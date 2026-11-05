// Package workershttp will serve the Workers endpoints, v1 and v2: worker
// definitions, test, execute and rerun.
//
// # Role
//
// workers module, adapter layer (HTTP). The package is named workershttp so
// that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests through the generated strict server in
// platform/gen/worker, including the v2 endpoints that plan P17 specifies
// from the jf worker plugin source (target layout, lines 687 and 696).
//
// # Outputs
//
// None yet. Planned (target layout, line 687): strict_worker.go, the module's
// part of that API, composed into the app router as a partial strict server
// (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 687 and 696, and "Generated
//     code and API specifications", line 815.
//   - Plan: docs/implementation/plan.md#p17-workers.
//   - Context map: the workers row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P17.
package workershttp
