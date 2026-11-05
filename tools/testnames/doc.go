// Command testnames checks that every Go test entry point declares its kind.
// It parses source files directly, including files excluded by the current
// operating system or build tags.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. make
// test-names, make test and the impact job of the quality gate run it.
//
// # Inputs
//
//   - The _test.go files under api/, internal/, migration/, pkg/, test/ and
//     tools/ below the repository root (-root).
//   - The reviewed owners of TestMain hooks and Fuzz targets, which are the
//     only entry points without a TestUnit or TestAcc prefix.
//
// # Outputs
//
//   - A failure that names each unclassified test and each unowned hook or
//     fuzz target.
//   - The counts of each kind, or with -json the complete inventory.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/ci.md. "Go test kinds".
//   - https://github.com/weaveplatform/artifactweave/issues/809
//
// # Status
//
// Implemented P9c (issue 809); P9d added migration/ to the scan.
package main
