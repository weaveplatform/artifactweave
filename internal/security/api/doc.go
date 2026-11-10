// Package api will be the security module's published interface.
//
// # Role
//
// security module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
// None yet. The target layout names the package without its contracts (line
// 358). Plan P26 integrates the AppTrust security-input contract (task 3).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 358.
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
package api
