// Package gems runs the RubyGems and Bundler routes of the assembled server
// across local, remote, federated and virtual repositories.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// gems.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     built over an in-memory store and a file-system blob store, with the
//     request and seed helpers of test/server/support.
//   - internal/artifacts/adapters/upstream/fakeupstream, which stands in
//     for the remote repositories' upstream.
//
// # Outputs
//
//   - TestAcc results for push and permissions, metadata, yanks, token
//     authentication, remote caching and its checks, virtual members, and
//     indexes that fail closed when a stored archive is missing or corrupt.
//   - TestAcc results for the download policy of local, remote and virtual
//     indexes and gems (issues 1237 and 1251), and, in
//     gems_virtual_download_test.go, a virtual gem download that asks each
//     member for the gem's own path.
//   - TestAccRubyClientsInstallPinnedRake runs the gem and Bundler clients
//     when they are installed, and skips otherwise.
//
// # References
//
//   - docs/testing/gems.md.
//   - https://github.com/weaveplatform/artifactweave/issues/764
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P9c (issue 764).
package gems
