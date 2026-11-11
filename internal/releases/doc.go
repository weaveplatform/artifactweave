// Package releases will be the releases module: release bundles v1 and v2,
// lifecycle stages, promotion and distribution to edges.
//
// # Role
//
// releases module, module root. Only app imports a module root; other modules
// reach releases through releases/api (decision 0044, dependency rules). Its
// storage skeleton (the module's ports, models and store adapters) sits in
// subpackages ahead of its phase; skeleton modules get no constructor until
// then (decision 0044, composition and HTTP).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New.
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps). The module's layers are its
// subpackages: api, core/bundlev1, core/distribution and core/signing (P15),
// core/bundlev2 and core/lifecycle (P24), and adapters/http (P15, P24).
//
// # References
//
//   - Context map, releases row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns release bundles v1 and v2, lifecycle stages, promotion and
//     distribution to edges; phases P15 and P24.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 348-353.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution,
//     #p24-lifecycle-and-release-bundles-v2.
//
// # Status
//
// Placeholder: implemented by P15, P24.
package releases
