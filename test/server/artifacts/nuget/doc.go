// Package nuget runs the NuGet routes of the assembled server: the route and
// principal matrix, the status table, API keys, client writes, reindex, the
// v3 remote and the symbol server.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// nuget.
//
// # Inputs
//
//   - The production router from internal/server, built over an in-memory
//     store and a file-system blob store, with the request and seed helpers
//     of test/server/support.
//   - migration/artifacts/adapters/upstream/fakeupstream, which stands in
//     for a v3 remote's upstream.
//   - support.NuGetRoutes, the route inventory, and the NuGet package and PDB
//     fixtures.
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in migration/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission,
//     beside which the tests retype the remote or add the repositories they
//     need.
//
// # Outputs
//
//   - TestAccNuGetRouteTablesCoverImplemented fails while a route of
//     support.NuGetRoutes has no status row or no principal row.
//   - TestAcc results for API keys, forced authentication, client writes,
//     reindex, the v3 remote and the symbol server's lifecycle, and for the
//     remote reindex API over a cached remote (nuget_reindex_test.go).
//
// # References
//
//   - Decision 0038: docs/research/decisions/0038-nuget.md.
//   - Research 29: docs/research/29-nuget.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from internal/server (issue 673),
// and P9d the remote reindex suite from migration/app (TST-07, issue 1096).
package nuget
