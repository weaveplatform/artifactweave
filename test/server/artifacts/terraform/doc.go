// Package terraform runs the Terraform registry routes of the assembled
// server: the route and principal matrix, the status tables, module init,
// provider install and jf terraform publish.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// terraform.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.TerraformRoutes, the route inventory, with
//     support.TerraformRegistryRepos and support.TerraformProviderRepos.
//
// # Outputs
//
//   - TestAccTerraformRouteTablesCoverImplemented fails while a route of
//     support.TerraformRoutes has no status row or no principal row.
//   - TestAcc results for module init, provider install, jf terraform publish
//     and the terraform type on the repositories API.
//
// # References
//
//   - Decision 0040: docs/research/decisions/0040-terraform.md.
//   - Research 31: docs/research/31-terraform.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from the server root (issue 673).
package terraform
