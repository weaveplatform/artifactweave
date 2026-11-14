// Package debian runs the Debian snapshot routes of the assembled server,
// with their read guards and their refusals of unavailable or corrupt
// indexes, a remote Debian repository's relay of upstream files, its .ddeb
// cache, its indexes cached as fetched, its component files and its -cache
// alias's metadata, index reads with a query or matrix parameters, a
// malformed package stored unindexed with and without metadata validation,
// a Debian repository's configuration, and the async values of the Debian
// reindex.
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
//   - debian_remote_query_test.go: a remote repository's Release files,
//     indexes and by-hash files, and a virtual repository's over it, asked
//     for with a query, served as without one with the upstream asked for
//     the path alone, or with the query under Propagate Query Params; a
//     matrix parameter still refused without asking (#1349); and an index's
//     by-hash copy served once cached to a caller who may read every
//     package it lists.
//   - debian_remote_as_fetched_test.go: a remote repository's index that
//     does not decode served and cached as fetched, on the remote and its
//     -cache alias, and left out by a virtual over it with one whose digest
//     its Release does not give (#1356).
//   - debian_remote_metadata_test.go: a remote repository's component
//     Release, Packages.diff/Index and slash distribution Release asked of
//     its upstream and served by a virtual over it (#1375), and a cached
//     Release, InRelease, installer index or component file served by its
//     -cache alias without asking the upstream (#1382).
//   - debian_remote_head_test.go: a HEAD of a remote repository's Packages
//     index, cold and warm, on a remote that stores it and one that does
//     not, answers its GET's length, type and cache headers (#1478).
//   - debian_repository_config_test.go: ddebSupported taken by a Debian
//     remote, and ddebSupported and debianTrivialLayout read as false by a
//     Debian local, federated or remote repository that never set them
//     (#1372).
//   - debian_metadata_validation_test.go: a package with an empty
//     Maintainer marked malformed with debian.metadata_validation on and
//     indexed with it off, and an unreadable one marked either way (#1374).
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
//     deploy (#1355), the package refused from a remote's upstream (#1331),
//     and the index cached as fetched and left out by a virtual over that
//     remote (#1356).
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
//     https://github.com/weaveplatform/artifactweave/issues/1352,
//     https://github.com/weaveplatform/artifactweave/issues/1355,
//     https://github.com/weaveplatform/artifactweave/issues/1356,
//     https://github.com/weaveplatform/artifactweave/issues/1372,
//     https://github.com/weaveplatform/artifactweave/issues/1374,
//     https://github.com/weaveplatform/artifactweave/issues/1375 and
//     https://github.com/weaveplatform/artifactweave/issues/1382
//
// # Status
//
// Implemented P9c (the plan's P29 group). P9d moved the router-level tests
// here from the server root (TST-06), and the tests that call the protocol's
// unexported code moved with it to internal/artifacts/ecosystem/debian/http
// (WEB-14).
package debian
