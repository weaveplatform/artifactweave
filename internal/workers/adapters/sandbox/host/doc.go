// Package host will be the helper-process side of the sandbox: the goja
// runtime, the platform client and allow-listed outbound HTTP.
//
// # Role
//
// workers module, adapter layer. The hidden worker-host subcommand of
// cmd/artifactweave calls its Main, so a script runs in a helper process and
// never in the server (target layout, lines 692 and 695).
//
// # Inputs
//
// None yet. Planned: execution frames from the parent over standard input
// (target layout, line 690).
//
// # Outputs
//
// None yet. Planned: execution results framed over standard output. A helper
// runs one execution at a time, so its heap growth during a run is that run's
// allocation and a memory cap can be enforced (decision 0026).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 692 and 695.
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
package host
