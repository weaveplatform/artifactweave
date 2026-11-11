// Package helm runs the classic Helm repository routes of the assembled
// server: the route and principal matrix and the status table.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// helm.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.HelmRoutes, the route inventory, and support.HelmChart.
//
// # Outputs
//
//   - TestAccHelmRouteTablesCoverImplemented fails while a route of
//     support.HelmRoutes has no status row or no principal row.
//
// # References
//
//   - Decision 0039: docs/research/decisions/0039-helm.md.
//   - Research 30: docs/research/30-helm.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from the server root (issue 673).
package helm
