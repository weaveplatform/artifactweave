// Package conda runs the Conda routes of the assembled server: the native
// channel, its indexes and their trust boundary, remote and virtual
// repositories, and CEP-16 shards.
//
// # Role
//
// Test support: server-assembly suites for the Conda ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is conda.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission,
//     beside which the tests retype the remote or add the repositories they
//     need.
//   - The controlled and conda-forge fixtures in
//     internal/artifacts/ecosystem/conda/testdata, read root-relative
//     through repopath, and remote sources over stub backends
//     (sourceIdentity, sourceBackend).
//
// # Outputs
//
// Assertions only:
//   - conda_test.go: index read failures, unsafe requests and virtual
//     cycles, the remote's filtering by current source authority, corrupt
//     and incompatible local indexes, the remote index and archive trust
//     boundary, and the shard endpoints with their permissions, transfers,
//     checksum reuse and trash restore.
//   - conda_remote_test.go: a remote's CEP-16 shards through refresh and
//     offline answers, an invalid shard that is neither published, cached
//     nor fallen back from, and shard fetches that recheck the caller's
//     permission and the repository's revision.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - docs/operations/conda.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068 and
//     https://github.com/weaveplatform/artifactweave/issues/1096
//
// # Status
//
// Implemented P9c (the plan's P32 group). P9d moved the tests here from the
// server root (TST-06) and the remote suite from internal/app (TST-07).
package conda
