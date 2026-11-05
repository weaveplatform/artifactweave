// Command coverexec runs one test binary for make test (go test -exec),
// moving its coverage output into a directory of its own under the one
// -test.gocoverdir names. make test then merges those directories.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// Makefile's test, accept-go and conan-accept targets build and use it.
//
// # Inputs
//
// The test binary and its arguments, as go test -exec passes them:
//
//	coverexec <test binary> [args...]
//
// # Outputs
//
//   - The test binary's own exit status and output.
//   - Its coverage files in a new directory inside the one the last
//     -test.gocoverdir argument names; arguments without that flag pass
//     through unchanged.
//
// # Invariants
//
// Test binaries built with -coverpkg=<module>/... that link the same
// instrumented packages write the same covmeta.<hash> file name. In one
// shared directory, two that finish together each rename a temporary file
// onto that name (internal/coverage/cfile, emitMetaDataFile), and Windows
// refuses the second rename with "Access is denied" (issue 368).
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/ci.md.
//   - https://github.com/weaveplatform/artifactweave/issues/368
//
// # Status
//
// Implemented P7 (issue 368).
package main
