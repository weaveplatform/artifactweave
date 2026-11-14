// Package npm runs the npm routes of the assembled server: the route and
// principal matrix, the status tables, login and logout, the auth lines,
// reindex and the layout conversion.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// npm.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - support.NPMRoutes, the route inventory, with support.NPMPublishBody and
//     support.NPMTarballOf.
//
// # Outputs
//
//   - TestAccNPMRouteTablesCoverImplemented fails while a route of
//     support.NPMRoutes has no status row or no principal row.
//   - TestAcc results for login and logout (no logout token is logged), the
//     auth lines, the queued reindex and the strict-layout conversion.
//   - TestAccNPMRemoteTarballMIMERefusal fails while an upstream tarball
//     whose label the remote's block list refuses answers other than 7.x's
//     409, remembered, without asking the upstream again (issue 1392).
//   - TestAccNPMRemoteTarballTypedByPath fails while a tarball of a remote
//     that stores nothing answers its upstream's label instead of the type
//     its kept copy answers (issue 1477).
//   - TestAccNPMRemoteTarballETagIsItsSHA1 and TestAccNPMRemoteKeysETagIsItsSHA1
//     fail while a remote tarball or keys document answers with its
//     upstream's ETag instead of its SHA-1, or matches it (issue 1484);
//     TestAccNPMRemoteTarballHeadWithoutChecksumIsMatchedByDate while a
//     tarball HEAD answered from the upstream's HEAD without a checksum is
//     matched by Go's rules against the upstream's ETag (issue 1487).
//
// # References
//
//   - Decision 0034: docs/research/decisions/0034-npm.md.
//   - Research 25: docs/research/25-npm.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P7; P9b moved the tests here from the server root (issue 673).
package npm
