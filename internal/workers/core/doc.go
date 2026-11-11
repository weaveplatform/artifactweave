// Package core will hold the workers module's logic: the action catalogue,
// bindings, gates, event subscriptions, executions, history and limits.
//
// # Role
//
// workers module, core layer. It offers the artifacts and access hooks by
// implementing the Gate interfaces of their api packages, so neither module
// imports workers (target layout, lines 700-715).
//
// # Inputs
//
// None yet. Planned: gate calls from artifacts and access before downloads,
// uploads and token creation, and artifacts and access events for the AFTER_*
// actions (target layout, lines 680-681).
//
// # Outputs
//
// None yet. Planned (target layout, lines 677-685):
//
//   - catalogue.go: the action catalogue, such as BEFORE_DOWNLOAD,
//     AFTER_CREATE and BEFORE_CREATE_TOKEN.
//   - binding.go: which workers run for which repositories and filters.
//   - gate.go and subscribe.go: BEFORE_* workers run synchronously, AFTER_*
//     workers from events.
//   - execute.go, history.go and limits.go: one execution with its payload,
//     limits and result mapping; execution history and rerun; the default
//     limits of decision 0026.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 676-685 and 700-715, and "A
//     BEFORE_DOWNLOAD worker blocks a download (P17)", lines 926-937.
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
package core
