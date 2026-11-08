// Package agentplugins runs the Agent Plugins routes of the assembled server:
// the marketplace files that track the repository tree, and their
// permissions.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// agentplugins.
//
// # Inputs
//
//   - The world of test/server/support/agentcommon.
//
// # Outputs
//
//   - TestAccAgentPluginMarketplacesTrackTreeAndPermissions: the marketplace
//     files follow the plugins as they are deployed, copied and deleted, and
//     each principal reads only what it may.
//
// # References
//
//   - Research 33: docs/research/33-package-formats.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P9b, which also moved the tests here from internal/server
// (issue 673).
package agentplugins
