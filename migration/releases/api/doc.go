// Package api will be the releases module's published interface: bundles,
// lifecycle, distribution and the module's events.
//
// # Role
//
// releases module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
// None yet. Planned (target layout, line 350): releases.go with the Bundles,
// Lifecycle and Distribution contracts, and events.go with BundleCreated,
// Promoted and Distributed.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 350.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution,
//     #p24-lifecycle-and-release-bundles-v2.
//   - Context map: the releases row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P15, P24.
package api
