// Package operations will be the operations module: health, metrics, support
// bundles, external integrations and queue administration.
//
// # Role
//
// operations module, module root. The target layout keeps this module flat:
// its logic will sit in this directory, beside integrations/ and
// adapters/http, with no api or core packages. Only app imports a module
// root, and other modules import a module only through its api package, so
// app alone reaches operations (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned: the health registry and metrics of platform/observe
// (P18), and the persistent work queue and the event replay that queue
// administration operates (P38; target layout, lines 168 and 728).
//
// # Outputs
//
// None yet. Planned, in this directory (target layout, line 360): health.go,
// metrics.go, support.go and queueadmin.go, with the module's constructor.
//
// # References
//
//   - Context map, operations row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns health, metrics, support bundles, external integrations and queue
//     administration; phases P18 and P38.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 360-361.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0016:
//     docs/research/decisions/0016-observability-and-health.md.
//   - Plan: docs/implementation/plan.md#p18-hardening-and-release,
//     #p38-operations-and-external-integrations.
//
// # Status
//
// Placeholder: implemented by P18, P38.
package operations
