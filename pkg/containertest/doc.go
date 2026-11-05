// Package containertest gates the tests that start containers (decision
// 0015) and starts the blob-store emulators they use.
//
// # Role
//
// Test support, outside the modules, for every package whose tests need
// Docker. They need a Docker daemon that runs Linux containers: GitHub's
// Windows runners have a healthy Docker that runs only Windows containers,
// so testcontainers' own health check passes and every Linux image then
// fails to start. Those tests skip there and run on Linux and on macOS with
// Docker Desktop.
//
// # Inputs
//
//   - The Docker engine that testcontainers finds, asked once per test
//     binary, within AnswerTimeout, whether it runs Linux containers.
//   - The -short flag: a short run skips every container test.
//
// # Outputs
//
//   - RequireLinux, which skips a test, and CheckLinux, which returns an
//     error, when no usable engine answers.
//   - MinIO, FakeGCS and Azurite, and RunMinIO, RunFakeGCS and RunAzurite
//     for callers without a testing.T: one emulator container per caller,
//     from an image pinned by digest, removed when the caller ends.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0027 and research 18: docs/research/18-cloud-blob-storage.md,
//     the emulators the cloud blob backends are tested against.
//   - https://github.com/weaveplatform/artifactweave/issues/458: an engine
//     that accepts connections and never answers.
//
// # Status
//
// Implemented P2 (the Linux engine gate), P2b (the emulators) and P7 (the
// answer timeout).
package containertest
