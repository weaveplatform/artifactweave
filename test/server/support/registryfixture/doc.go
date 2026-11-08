// Package registryfixture assembles the real server for registry protocol
// suites. Docker and Helm OCI use the same repository, auth and blob-signing
// boundary without importing root-package test helpers.
//
// # Role
//
// Test support, outside the modules, for test/server/artifacts/docker and
// test/server/artifacts/helmoci.
//
// # Inputs
//
//   - The production router from internal/server, an in-memory store and a
//     file-system blob store, and the seeding of test/server/support.
//   - Callbacks that adjust the server configuration once the stores and
//     authentication are ready.
//
// # Outputs
//
//   - World, from New: the router with the users reader, deployer and user;
//     local, remote and virtual repositories; anonymous read access when
//     asked for; and a blob store that signs download URLs for the storage
//     route.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/673
//
// # Status
//
// Implemented P9b (issue 673).
package registryfixture
