// Package debian runs the Debian snapshot routes of the assembled server,
// with their read guards and their refusals of unavailable or corrupt
// indexes, a remote Debian repository's relay of upstream files, its .ddeb
// cache, index reads with a query or matrix parameters, a malformed
// package stored unindexed, and the async values of the Debian reindex.
//
// # Role
//
// Test support: server-assembly suites for the Debian ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is debian.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The harness's remote world (harness.NewRemoteWorld): a remote
//     repository over a chi upstream archive the test serves, through the
//     remote cache and its validation.
//
// # Outputs
//
// Assertions only:
//   - debian_snapshot_security_test.go: snapshot read guards, and
//     unavailable or corrupt indexes refused.
//   - debian_remote_upstream_test.go: the files apt reads that a remote
//     asks its upstream for and serves (#1273), and a by-hash file whose
//     bytes do not match its name refused and not cached.
//   - debian_index_query_test.go: a local, federated, remote and virtual
//     repository's indexes, and a virtual repository's other files, asked
//     for with a query or matrix parameters by an administrator, a reader,
//     a user without read and an anonymous caller (#1330).
//   - debian_remote_query_test.go: a remote repository's indexes and
//     by-hash file, and a virtual repository's over it, asked for with a
//     query, served as without one with the upstream asked for the path
//     alone, and a matrix parameter still refused without asking (#1349).
//   - debian_remote_ddeb_test.go: a remote repository's .ddeb served and
//     cached, and a local or federated .ddeb deploy refused without
//     ddebSupported (#1352).
//   - debian_malformed_test.go: a package that is not readable stored by a
//     local or federated repository with deb.index.status, left out of
//     Packages and Release, found by AQL, and indexed once a readable
//     package replaces it (#1355).
//   - debian_reindex_async_test.go: Calculate Debian Repository Metadata
//     with each async value, queued or run before it answers, or refused.
//   - debian_xz_test.go: a package whose control.tar.xz or data.tar.xz,
//     or a Packages.xz, lost its xz index and footer, stored unindexed at
//     deploy (#1355), and refused from a remote's upstream and through a
//     virtual over that remote (#1331).
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/debian.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068,
//     https://github.com/weaveplatform/artifactweave/issues/1083,
//     https://github.com/weaveplatform/artifactweave/issues/1273,
//     https://github.com/weaveplatform/artifactweave/issues/1331,
//     https://github.com/weaveplatform/artifactweave/issues/1349,
//     https://github.com/weaveplatform/artifactweave/issues/1352 and
//     https://github.com/weaveplatform/artifactweave/issues/1355
//
// # Status
//
// Implemented P9c (the plan's P29 group). P9d moved the router-level tests
// here from the server root (TST-06), and the tests that call the protocol's
// unexported code moved with it to internal/artifacts/ecosystem/debian/http
// (WEB-14).
package debian
