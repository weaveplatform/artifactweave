// Package operationshttp will serve the operations endpoints: health,
// metrics, support bundles and queue administration.
//
// # Role
//
// operations module, adapter layer (HTTP). The package is named
// operationshttp so that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests for metrics and health (plan P18, task 1), and
// for support bundles, system information, logs and persistent-work
// administration (plan P38, task 2).
//
// # Outputs
//
// None yet. Planned: those endpoints over the operations module and
// platform/observe (target layout, lines 360-361).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 361.
//   - Plan: docs/implementation/plan.md#p18-hardening-and-release,
//     #p38-operations-and-external-integrations.
//   - Context map: the operations row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0016:
//     docs/research/decisions/0016-observability-and-health.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P18, P38.
package operationshttp
