// Package terraformbackend runs the Terraform remote state routes of the
// assembled server: the route and principal matrix, the status table and
// state read through the item routes.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// terraformbackend.
//
// # Inputs
//
//   - The production router from internal/server, built over an in-memory
//     store and a file-system blob store, with the request and seed helpers
//     of test/server/support.
//   - support.TerraformBackendRoutes, the route inventory, with
//     support.TerraformBackendRepos and support.TerraformBackendKeys, the
//     keyring of the sealed fixture state.
//
// # Outputs
//
//   - TestAccTerraformBackendRouteTablesCoverImplemented fails while a route
//     of support.TerraformBackendRoutes has no status row or no principal
//     row.
//   - TestAccTerraformBackendThroughTheItemRoutes: state the backend stores
//     is reachable through the item routes.
//
// # References
//
//   - Decision 0041: docs/research/decisions/0041-terraform-backend.md.
//   - Research 32: docs/research/32-terraform-backend.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from internal/server (issue 673).
package terraformbackend
