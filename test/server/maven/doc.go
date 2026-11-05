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
//   - The production router from internal/server, built over an in-memory
//     store and a file-system blob store, with the request and seed helpers
//     of test/server/support.
//   - support.MavenRoutes, the route inventory.
//
// # Outputs
//
//   - TestAccMavenRouteTablesCoverImplemented fails while a route of
//     support.MavenRoutes has no status row or no principal row.
//   - TestAcc results for snapshot deploys, the metadata work a deploy queues
//     and the next read runs, version resolution, Latin-1 POMs, the layout
//     without integration revisions, and calculateMetadata and generatePom
//     over HTTP.
//
// # References
//
//   - Decision 0036: docs/research/decisions/0036-maven.md.
//   - Research 27: docs/research/27-maven.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P7; P9b moved the tests here from internal/server (issue 673).
package maven
