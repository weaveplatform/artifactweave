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
//   - The harness's remote world (harness.NewRemoteWorld), its remote made
//     a Chef remote over a fake Supermarket that serves at most 100
//     cookbooks a catalog or search page whatever items asks
//     (chef_remote_paging_test.go, cappedSupermarket), and a front for it
//     that records every GET the remote sends (chef_remote_catalog_test.go,
//     countedSupermarket).
//
// # Outputs
//
// Assertions only:
//   - chef_item_test.go: cached metadata has no generic download bypass and
//     cannot escape by transfer, checksum deploy or restore.
//   - chef_remote_paging_test.go: a remote reads the 100-cookbook pages of
//     an upstream catalog that a page of its own covers, and forwards a
//     search to the upstream's search, through the remote and a virtual
//     repository over it; a catalog of at most 100 keeps its answers, and a
//     short first page fails with 502.
//   - chef_remote_catalog_test.go: over an upstream of 4,200 cookbooks,
//     Knife's list and search are answered from the upstream pages they
//     cover, items=9999999 clamped to a page of 1,000, a page past 4,096
//     served, order and user not forwarded, and no cookbook or version
//     record read.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1298
//   - https://github.com/weaveplatform/artifactweave/issues/1305 and
//     https://github.com/weaveplatform/artifactweave/issues/1306
//   - JFrog, "Search for Chef Cookbooks" (Chef Cookbook Repositories): a
//     remote's search runs on its cache, then is forwarded upstream and
//     the results merged.
//
// # Status
//
// Implemented P9c (the plan's P33 group). P9d moved the tests here from the
// server root (TST-06).
package chef
