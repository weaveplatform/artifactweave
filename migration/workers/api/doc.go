// Package api will be the workers module's published interface: worker
// administration and execution, and the module's events.
//
// # Role
//
// workers module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned callers: gateway/console and the operations module
// (target layout, line 674).
//
// # Outputs
//
// None yet. Planned (target layout, lines 673-675): workers.go with Workers
// (List, Get, Put, Delete, Execute, Test), and events.go with ExecutionFailed
// and WorkerChanged.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 673-675.
//   - Plan: docs/implementation/plan.md#p17-workers.
//   - Context map: the workers row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P17.
package api
