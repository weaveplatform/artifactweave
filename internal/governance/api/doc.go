// Package api will be the governance module's published interface.
//
// # Role
//
// governance module, api layer. It may import only kernel, platform/outbox
// and its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
// None yet. The target layout names the package without its contracts (line
// 357). Plan P25 exposes the module's decisions to automation as well as to
// its own endpoints (task 3).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 357.
//   - Plan: docs/implementation/plan.md#p25-apptrust-and-policy-governance.
//   - Context map: the governance row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P25.
package api
