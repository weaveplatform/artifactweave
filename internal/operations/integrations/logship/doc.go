// Package logship will ship logs to external log services and cloud log
// collection.
//
// # Role
//
// operations module, adapter layer for an external integration. The module is
// flat, so its integrations sit under integrations/ beside its own files.
//
// # Inputs
//
// None yet. Planned: log shipping settings and the server's logs (plan P38,
// task 1).
//
// # Outputs
//
// None yet. Planned: logs delivered to the configured service, with secrets
// redacted and failures observable (plan P38, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 361.
//   - Plan:
//     docs/implementation/plan.md#p38-operations-and-external-integrations.
//   - Context map: the operations row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P38.
package logship
