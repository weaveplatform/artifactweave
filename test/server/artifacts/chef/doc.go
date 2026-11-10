// Package chef runs the Chef routes of the assembled server: cached remote
// cookbook metadata and the generic paths that must not bypass it.
//
// # Role
//
// Test support: server-assembly suites for the Chef ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is chef.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - A remote Chef repository over a stub source (sourceIdentity,
//     sourceBackend).
//
// # Outputs
//
// Assertions only:
//   - chef_item_test.go: cached metadata has no generic download bypass and
//     cannot escape by transfer, checksum deploy or restore.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - docs/testing/chef.md.
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
// Implemented P9c (the plan's P33 group). P9d moved the tests here from
// internal/server (TST-06).
package chef
