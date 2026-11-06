// Package workers will be the workers module: the sandbox runtime, the worker
// catalogue, executions and hook bindings.
//
// # Role
//
// workers module, module root. Only app imports a module root; other modules
// reach workers through workers/api (decision 0044, dependency rules). Its
// storage skeleton (the module's ports, models and store adapters) sits in
// subpackages ahead of its phase; skeleton modules get no constructor until
// then (decision 0044, composition and HTTP).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New, among them the
// artifacts/api Items, the access/api Authorizer and the event bus (target
// layout, line 672).
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps), and the module's gates, which
// app registers on artifacts and access (target layout, lines 713-714 and
// 801-802). The module's layers are its subpackages: api, core, adapters/http
// and adapters/sandbox.
//
// # References
//
//   - Context map, workers row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns the sandbox runtime, the worker catalogue, executions and hook
//     bindings; phases P17.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Workers
//     (P17), a roadmap domain end to end", lines 668-715, and "A
//     BEFORE_DOWNLOAD worker blocks a download (P17)", lines 926-937.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Plan: docs/implementation/plan.md#p17-workers.
//
// # Status
//
// Placeholder: implemented by P17.
package workers
