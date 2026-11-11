// Package releasebundles will hold the storage rules of the releasebundles
// repository type.
//
// # Role
//
// artifacts module, ecosystem layer: one slice per package type. The releases
// module writes into this repository type only through artifacts/api.Items.
//
// # Inputs
//
// None yet. Planned: the deploys that the releases module makes through
// artifacts/api.Items when it stores a bundle (decision 0023).
//
// # Outputs
//
// None yet. Planned: the format rules of the releasebundles package type,
// registered in the ecosystem registry like every other slice (target layout,
// lines 237-238 and 387).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 237-238 and 387.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution.
//   - Context map: the artifacts row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P15.
package releasebundles
