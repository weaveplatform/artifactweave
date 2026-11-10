// Package evidence will be the evidence module: attestations, verification
// and trusted signers.
//
// # Role
//
// evidence module, module root. Only app imports a module root; other modules
// reach evidence through evidence/api (decision 0044, dependency rules). The
// module has no code before P23; its root and layers arrive together.
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New: the unit of work, the
// SQL handle, the outbox and event bus, the access/api Authorizer and Keys,
// the artifacts/api Items and the releases/api Bundles (target layout, line
// 961).
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps). The module hears about copies,
// deletes and promotions through events, so no other module's core changes
// for it (target layout, lines 965 and 973-974). Its layers are its
// subpackages: api, core, adapters/http and adapters/sql.
//
// # References
//
//   - Context map, evidence row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns attestations, verification and trusted signers; phases P23.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 356, and "Adding a new business domain: Evidence (P23)",
//     lines 957-974.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Plan: docs/implementation/plan.md#p23-evidence-and-provenance.
//
// # Status
//
// Placeholder: implemented by P23.
package evidence
