// Package pypi runs the PyPI routes of the assembled server: the route and
// principal matrix, the status table, the JSON routes, uploads, the pip
// install sequence and reindex.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// pypi.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.PyPIRoutes, the route inventory.
//
// # Outputs
//
//   - TestAccPypiRouteTablesCoverImplemented fails while a route of
//     support.PyPIRoutes has no status row or no principal row.
//   - TestAcc results for the JSON routes, uploads with Basic or token
//     credentials, PUT through the item deploy, the pip install sequence and
//     reindex.
//   - TestAccPypiRemoteFileMIMERefusal fails while an upstream file whose
//     label the remote's block list refuses answers other than 7.x's 409,
//     remembered, without asking the upstream again (issue 1392).
//
// # References
//
//   - Decision 0037: docs/research/decisions/0037-pypi.md.
//   - Research 28: docs/research/28-pypi.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from the server root (issue 673).
package pypi
