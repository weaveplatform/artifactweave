// Package sandbox will run worker code in helper processes: a pool with one
// execution per helper, the parent side of the pipe protocol, and TypeScript
// transpilation.
//
// # Role
//
// workers module, adapter layer. It implements the Sandbox port that
// workers/core/port declares.
//
// # Inputs
//
// None yet. Planned: executions from workers/core, each with its compiled
// program, payload and limits (target layout, lines 688-693).
//
// # Outputs
//
// None yet. Planned (target layout, lines 689-691):
//
//   - pool.go: the helper process pool, one execution per helper.
//   - protocol.go: parent and helper framing over the helper's standard input
//     and output.
//   - transpile.go: esbuild from TypeScript to JavaScript.
//
// Each execution's result comes back over the pipe. A looping or
// over-allocating worker is stopped by its limits while the server's heap is
// unaffected (decision 0026; plan P17, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 688-693.
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
package sandbox
