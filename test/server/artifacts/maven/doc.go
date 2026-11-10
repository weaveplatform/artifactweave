// Package maven runs the Maven routes of the assembled server: the route and
// principal matrix, the status table, snapshot deploys, metadata rebuilt on
// read, version resolution and the calculateMetadata and generatePom
// operations.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// maven.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.MavenRoutes, the route inventory.
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission,
//     beside which the tests retype the remote or add the repositories they
//     need.
//   - Runtimes the restart test opens and closes itself (testConfig and
//     openRuntime over the harness's configuration and costs).
//
// # Outputs
//
//   - TestAccMavenRouteTablesCoverImplemented fails while a route of
//     support.MavenRoutes has no status row or no principal row.
//   - TestAcc results for snapshot deploys, the metadata work a deploy queues
//     and the next read runs, version resolution, Latin-1 POMs, the layout
//     without integration revisions, and calculateMetadata and generatePom
//     over HTTP.
//   - TestAcc results for the Maven remote (maven_remote_test.go): metadata,
//     artifacts and outages, snapshot and release resolution, denials and
//     admission, checksum policies, eager companions and their retention,
//     which survive a restart, and the family layouts; and for mapped
//     remotes (mapped_remote_test.go): descriptor downloads, alias denial,
//     and the publication and delivery fences of a mapped root.
//
// # References
//
//   - Decision 0036: docs/research/decisions/0036-maven.md.
//   - Research 27: docs/research/27-maven.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md;
//     docs/operations/maven-remote-repositories.md and
//     docs/operations/mapped-remote-discovery.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P7; P9b moved the tests here from the server root (issue 673),
// and P9d the remote suites from internal/app (TST-07, issue 1096).
package maven
