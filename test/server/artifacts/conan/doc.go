// Package conan runs the Conan routes of the assembled server: revisions,
// search, uploads and their refusals, virtual and remote repositories, and
// the pinned Conan 2 and Conan 1 clients.
//
// # Role
//
// Test support: server-assembly suites for the Conan ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is conan.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - Remote sources over stub backends (sourceIdentity, sourceBackend),
//     Conan archives from support.TarGz (conanTestArchive), and a token
//     service for the clients' login (seedTokens).
//   - The Conan 2 and Conan 1 binaries named by ARTIFACTWEAVE_CONAN2_BIN and
//     ARTIFACTWEAVE_CONAN1_BIN; TestAccConanClients skips a generation whose
//     variable is unset. make conan-accept runs the TestAccConan tests here
//     and in migration/app with coverage.
//
// # Outputs
//
// Assertions only:
//   - conan_test.go: search and package deletion, revision ordering, route
//     and class policy, manifest and upload refusals, virtual members, the
//     authentication boundary, the native revision lifecycle and the remote
//     and virtual source with its failures.
//   - conan_client_test.go: create, upload, remove and download with each
//     client generation on local, federated and virtual repositories.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/conan.md (the clients, the commit marker and the
//     qualification) and docs/client-recipes/conan.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068
//
// # Status
//
// Implemented P9c (the plan's P31 group). P9d moved the tests here from
// internal/server (TST-06).
package conan
