// Package rpm runs the RPM routes of the assembled server: the signing of
// virtual repository metadata.
//
// # Role
//
// Test support: server-assembly suites for the RPM ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is rpm.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//
// # Outputs
//
// Assertions only:
//   - rpm_test.go: virtual metadata is signed with the configured GPG key.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/rpm.md.
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
// Implemented P9c (the plan's P29 group). P9d moved the router-level tests
// here from internal/server (TST-06); the tests that call unexported server
// code stay there as rpm_internal_test.go.
package rpm
