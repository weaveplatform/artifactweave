// Package security will be the security module: indexing, scanning, SBOMs,
// findings, watches, Catalog and Curation admission.
//
// # Role
//
// security module, module root. Only app imports a module root; other modules
// reach security through security/api (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New.
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps), and the module's Curation
// gate, which app registers on artifacts (target layout, lines 713-714 and
// 801-802). The module's layers are its subpackages: api, the seven core
// packages and the five adapters.
//
// # References
//
//   - Context map, security row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns indexing, scanning, SBOMs, findings, watches, Catalog and Curation
//     admission; phases P26 and P27.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 358-359.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Plan:
//     docs/implementation/plan.md#p26-security-scanning-and-intelligence,
//     #p27-advanced-security-and-curation.
//
// # Status
//
// Placeholder: implemented by P26, P27.
package security
