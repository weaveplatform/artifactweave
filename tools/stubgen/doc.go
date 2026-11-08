// Command stubgen writes migration/platform/gen/<service>/unimplemented.gen.go
// from the generated strict server interface (make generate).
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// work is migration/devtools/stubgen.
//
// # Inputs
//
// The server.gen.go files to read, one or more:
//
//	stubgen <path/to/server.gen.go>...
//
// # Outputs
//
//   - unimplemented.gen.go beside each input: a method for every operation of
//     the strict server interface that answers 501 until a handler implements
//     it.
//   - A line per input with the number of operations stubbed.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas, task
//     3.
//
// # Status
//
// Implemented P1.
package main
