// Package curation will hold Curation admission policy: which packages may
// enter through remote repositories, with exceptions and audited decisions.
//
// # Role
//
// security module, core layer.
//
// # Inputs
//
// None yet. Planned: admission policies and exceptions, and the packages that
// remote retrieval asks to admit (plan P27, task 2).
//
// # Outputs
//
// None yet. Planned: admission decisions, enforced through the Curation gate
// that app registers on artifacts, on cache misses and cached retrievals
// alike (target layout, line 714; plan P27, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 358, and "Workers (P17), a roadmap domain end to end", line
//     714.
//   - Plan: docs/implementation/plan.md#p27-advanced-security-and-curation.
//   - Context map: the security row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P27.
package curation
