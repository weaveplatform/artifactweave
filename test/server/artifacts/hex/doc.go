// Package hex runs the Hex routes of the assembled server: the signed
// registry of local, virtual and remote repositories, and the routes and
// classes it refuses.
//
// # Role
//
// Test support: server-assembly suites for the Hex ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is hex.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The decimal and ecto tarballs in
//     internal/artifacts/ecosystem/hex/testdata, read root-relative through
//     repopath, and two RSA key pairs, each generated once per test binary:
//     testKeys, and otherKeys for a virtual repository that signs with a
//     key its members do not hold.
//
// # Outputs
//
// Assertions only:
//   - hex_test.go: the local and virtual signed registry, invalid routes and
//     repository configuration, the federated class refused, and the
//     download policy on local and virtual repositories.
//   - hex_remote_test.go: a remote's signed metadata that filters a denied
//     version, a virtual repository over remote members, one that passes
//     over a member whose upstream lacks the package or that the caller may
//     not read, one whose own read permissions narrow what its members
//     list, and the download policy on remote repositories and remote
//     members.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - docs/implementation/hex-client.md.
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
// Implemented P9c (the plan's P30 group). P9d moved the tests here from the
// server root (TST-06).
package hex
