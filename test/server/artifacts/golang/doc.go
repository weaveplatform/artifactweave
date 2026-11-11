// Package golang runs the Go module proxy routes of the assembled server: the
// route and principal matrix and the status table.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// golang.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.GoRoutes, the route inventory.
//
// # Outputs
//
//   - TestAccGoRouteTablesCoverImplemented fails while a route of
//     support.GoRoutes has no status row or no principal row.
//
// # References
//
//   - Decision 0035: docs/research/decisions/0035-go-modules.md.
//   - Research 26: docs/research/26-go-modules.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P7; P9b moved the tests here from the server root (issue 673).
package golang
