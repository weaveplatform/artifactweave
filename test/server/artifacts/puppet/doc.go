// Package puppet runs the Puppet routes of the assembled application: a
// remote module cache through refresh, offline and integrity failures, and
// a local repository's registered routes, lists and reindex.
//
// # Role
//
// Test support: server-assembly suites for the Puppet ecosystem, outside
// the modules. They drive the production router over the application
// runtime through HTTP requests. The package is one of its own so that CI
// selection can run it alone; its owner in
// tools/citestselection/test_ownership.json is puppet.
//
// # Inputs
//
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote, which the
//     tests retype, the users reader and denied and the remote-read
//     permission.
//   - The Puppet module fixtures in test/acceptance/fixtures/puppet, read
//     root-relative through repopath, module archives the tests build, and
//     upstream Forge test servers.
//
// # Outputs
//
// Assertions only:
//   - puppet_remote_test.go: the remote cache's refresh, offline answers
//     and integrity checks, the routes a local Puppet repository registers
//     with their permissions and reindex, and the download policy of a
//     remote, and of a virtual repository over it, at the paths 7.x checks.
//   - puppet_list_test.go: a local repository's lists of every release and
//     module through the generated Get Puppet Releases and Get Puppet
//     Modules routes, with their permissions, and a virtual repository's
//     404.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/puppet.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1096
//
// # Status
//
// Implemented P9c. P9d moved the tests here from internal/app (TST-07).
package puppet
