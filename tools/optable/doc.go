// Command optable writes internal/platform/gen/operations/operations.gen.go,
// the table of every operation in the bundled specs with its route pattern
// and declared classes (make generate).
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary. The work is internal/devtools/optable.
//
// # Inputs
//
//   - The bundled specs, api/<service>.openapi.yaml (-api, api by default).
//
// # Outputs
//
//   - The generated table (-out, internal/platform/gen/operations/
//     operations.gen.go by default), in the package its directory names, and
//     a line with the number of operations written.
//
// # References
//
//   - Decision 0043, "Cost classes and how admission finds them" and "Replay
//     for keyed operations": docs/research/decisions/0043-api-reliability.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1405
//
// # Status
//
// Implemented #1405 (CON-02).
package main
