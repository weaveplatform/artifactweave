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
//   - puppet_remote_paging_test.go: a remote over a Forge that serves
//     fewer releases a page than it is asked for, as forgeapi.puppet.com
//     serves at most 100, reads every page through the remote and through
//     a virtual repository over it, and answers every route with the
//     Forge's release fields relayed and 7.x's pagination; the answers
//     kept for a module of at most 100 releases, and the 502 for a page
//     short of what its upstream states.
//   - puppet_module_groups_test.go: a Puppet Enterprise client's
//     module_groups on the release list and the search of a local and a
//     remote repository, never passed upstream, and a
//     remote's release that its upstream's release list leaves out,
//     answered from the upstream's record after the caller's permission
//     and the remote's policy.
//   - puppet_upstream_record_test.go: the archive of a release only the
//     Forge's record holds, through a remote, refused when it does not
//     match the record, and the record and archive through a virtual
//     repository over the remote, a local member's release winning; and
//     a remote's module record for a module the Forge's release list
//     leaves out, answered from the Forge's module record.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1096,
//     https://github.com/weaveplatform/artifactweave/issues/1343,
//     https://github.com/weaveplatform/artifactweave/issues/1344,
//     https://github.com/weaveplatform/artifactweave/issues/1369 and
//     https://github.com/weaveplatform/artifactweave/issues/1370
//
// # Status
//
// Implemented P9c. P9d moved the tests here from internal/app (TST-07).
package puppet
