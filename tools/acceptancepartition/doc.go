// Package acceptancepartition checks that the godog tag filters of the native
// acceptance jobs in .github/workflows/quality-gate.yml split the compiled
// Gherkin scenarios between them.
//
// # Role
//
// Repository tooling under tools/, outside the modules, like the commands
// beside it, but a package of tests only that guards the CI workflow. Native
// acceptance runs as several jobs to stay within the 15-minute budget, and a
// filter that dropped a scenario would pass without a sign.
//
// # Inputs
//
//   - The quality-gate workflow: each acceptance job's
//     ARTIFACTWEAVE_ACCEPT_TAGS filter, the jobs the coverage and gate jobs
//     wait for, and each job's coverage upload.
//   - The feature files under test/acceptance/features, compiled with every
//     Scenario Outline example expanded.
//
// # Outputs
//
//   - Test failures that name a job whose filter changed, a partition the
//     coverage or gate job does not wait for, a coverage upload allowed to be
//     empty, a scenario without its partition's tag, a partition that selects
//     no scenario, or a core scenario that runs the .NET client.
//   - With -v, the number of compiled scenarios in each partition.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/ci.md.
//   - https://github.com/weaveplatform/artifactweave/issues/705
//
// # Status
//
// Implemented P9b (issue 705).
//
// # Testing
//
//   - TestUnitWorkflowAcceptancePartitions.
package acceptancepartition
