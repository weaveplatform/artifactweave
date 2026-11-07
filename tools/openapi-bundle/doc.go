// Command openapi-bundle merges the api/src/<service>/*.yaml fragments into
// api/<service>.openapi.yaml (make bundle).
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// merging itself is migration/devtools/openapibundle; make generate runs the
// bundle before it generates code.
//
// # Inputs
//
//   - The fragment directory, one sub-directory per service (-src, api/src by
//     default).
//
// # Outputs
//
//   - One bundled document per service in the output directory (-out, api by
//     default), and a line per service on standard output.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas, task
//     3.
//
// # Status
//
// Implemented P1 and P1b (any number of services).
package main
