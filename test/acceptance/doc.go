// Package acceptance runs the Gherkin features in features/ against the real
// artifactweave binary (decision record 0015).
//
// # Role
//
// Acceptance suite, outside the modules: it reaches the server only through
// the built binary, its HTTP surface and the native clients.
//
// # Inputs
//
//   - The features under features/, with the step definitions in this
//     package.
//   - cmd/artifactweave, built once with -cover. When
//     ARTIFACTWEAVE_GOCOVERDIR is set (make accept), every process writes
//     coverage there, so acceptance runs count toward the merged gate;
//     ARTIFACTWEAVE_ACCEPT_COVERPKG narrows the instrumented packages.
//   - ARTIFACTWEAVE_ACCEPT_TAGS, the godog tags of one CI partition.
//   - The pinned native clients the scenarios run, and Docker for the client
//     containers and the blob-store emulators.
//
// # Outputs
//
//   - A result per scenario, and a failure when the tags select no scenario.
//   - The binary's coverage data.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/ci.md.
//   - docs/testing/ci-native-client-inventory.md.
//   - Plan: docs/implementation/plan.md#p0-foundations, task 8.
//
// # Status
//
// Implemented P0 to P9c: P0 built the harness and every later phase added its
// features.
//
// # Testing
//
// The suite runs on Linux. It skips on Windows, whose runners cannot run the
// container-backed scenarios, and on macOS, whose runners have no Docker,
// unless ARTIFACTWEAVE_ACCEPT_LOCAL=1 is set there. With the e2e build tag,
// TestAccRealUpstreams probes public registries (make e2e).
package acceptance
