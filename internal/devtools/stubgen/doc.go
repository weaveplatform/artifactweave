// Package stubgen writes a type that implements a generated strict server
// interface by answering 501 for every operation. oapi-codegen emits an
// Unimplemented type for the plain ServerInterface but not for the strict one,
// and the strict one is what the server implements; without a stub every
// scaffolded operation would have to be hand-stubbed before the tree compiles.
//
// # Role
//
// devtools layer: the library behind tools/stubgen, which make generate runs
// on each spec's generated server after oapi-codegen; never linked into the
// server binary. It imports only the standard library.
//
// # Inputs
//
//   - A server.gen.go that oapi-codegen generated with a strict server: its
//     package clause and the methods of its StrictServerInterface.
//
// # Outputs
//
//   - Parse: the package name and each method's name, request type and
//     response type.
//   - Generate: the source of the stub, formatted: a type UnimplementedStrict
//     whose every method returns apierror.ErrNotImplemented, which the
//     server's response error handler renders as 501, and an assertion that
//     the type implements the interface.
//   - Run: that source written as unimplemented.gen.go beside the input,
//     unless another path is given, and the number of operations stubbed.
//
// # Invariants
//
//   - The stubs are generated and committed: make generate rewrites them,
//     and make verify-generate fails when a committed stub differs from what
//     its server.gen.go gives (decision 0002).
//   - The assertion makes a wrong stub fail to compile rather than pass
//     unnoticed: a parameter or result type other than a name, a qualified
//     name or a pointer to one is written as any, and would no longer match
//     the interface.
//   - A stub with no operations imports nothing, so the generated package
//     of a spec whose paths are still empty compiles.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas, task
//     3.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 113 to 116 (the devtools tree) and line 1045 (the mapping row).
//   - https://github.com/weaveplatform/artifactweave/issues/990
//
// # Status
//
// Implemented P1. Staged in P9d.
//
// # Testing
//
//   - TestUnitParseAndGenerate: the stub for a fixture interface has a 501
//     method per operation and type-checks against the fixture package.
//   - TestUnitGenerateNoMethods: a stub with no operations imports nothing.
//   - TestUnitParseErrors, TestUnitRun, TestUnitTypeString: a file that is
//     not Go or has no strict interface, the default output path, read and
//     write failures, and the types written as any.
//
// The server's TestAccUnimplementedOperationsAnswer501 proves that the
// committed stubs answer 501 for every operation no handler implements.
package stubgen
