// Command citestselection selects the safe CI test profile for a pull
// request. Uncertain input always receives the full profile.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// impact job of the quality gate (.github/workflows/quality-gate.yml) runs it
// to plan the other jobs, and the affected-coverage job runs it again with
// -check-coverage.
//
// # Inputs
//
//   - The pull request's base and head commits (-base, -head), whether it is
//     the release-please pull request (-release-pr) and whether it carries
//     the ci:full label (-full-override).
//   - test_ownership.json, embedded in the binary: the reviewed owner of
//     every directory with server tests.
//   - The TestUnit and TestAcc tests it finds in the source under api/,
//     internal/, migration/, pkg/, test/ and tools/, and the source import
//     graph, which adds the packages that import a changed one.
//   - With -check-coverage and -packages: a merged coverage directory and the
//     changed production packages.
//
// # Outputs
//
//   - The profile (full, format, deployment or docs) and the reason for it,
//     with the selected unit packages, acceptance tags and native clients,
//     written to the GitHub Actions output and step summary files
//     (-github-output, -summary).
//   - With -selection-json, the selected Go test names and packages as JSON.
//   - With -check-coverage, a failure when a changed production package is
//     missing from the same run's coverage or below 90% of its statements.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/ci.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/673
//   - https://github.com/weaveplatform/artifactweave/issues/951
//
// # Status
//
// Implemented P9b (issue 673); P9d added the staged and split test packages
// (issue 951).
package main
