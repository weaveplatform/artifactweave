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
//     from an image pinned by digest, removed when the caller ends. MinIO
//     is handed out only once it serves S3 requests, not merely HTTP. Each
//     image is pulled before its module starts, one pull per image at a
//     time in the process. A pull that fails moves on to the image's second
//     source, a registry serving the same digest (none yet for Azurite),
//     and when every source fails the error names each registry's answer
//     rather than the "No such image" testcontainers' own pull ends in.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0027 and research 18: docs/research/18-cloud-blob-storage.md,
//     the emulators the cloud blob backends are tested against.
//   - https://github.com/weaveplatform/artifactweave/issues/458: an engine
//     that accepts connections and never answers.
//   - https://github.com/weaveplatform/artifactweave/issues/1307: MinIO
//     answering CreateBucket with 503 XMinioServerNotInitialized after the
//     module's liveness wait passed.
//   - https://github.com/weaveplatform/artifactweave/issues/1448 and
//     https://github.com/testcontainers/testcontainers-go/issues/3945: a
//     pull whose failure the engine reports inside its 200 answer, which
//     testcontainers-go does not decode.
//
// # Status
//
// Implemented P2 (the Linux engine gate), P2b (the emulators) and P7 (the
// answer timeout). MinIO's readiness wait added by issue 1307; the checked
// image pull and the second sources by issue 1448.
package containertest
