// Package agentplugins runs the Agent Plugins routes of the assembled server:
// the stored marketplace files that track the repository tree, and their
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
//   - The world of test/server/support/agentcommon, with the runtime's work
//     queue and item service (world_test.go), whose marketplace work a test
//     runs as a server's workers would once an upload has answered.
//
// # Outputs
//
//   - TestAccAgentPluginMarketplacesTrackTreeAndPermissions: the marketplace
//     files follow the plugins as they are deployed, copied and deleted, and
//     only a principal who may read a file's path reads it.
//   - TestAccAgentPluginStandardLayoutIsServed: a bundle with an Agent
//     Plugins Standard root plugin.json is admitted, typed and listed in the
//     VS Code, Cursor and unified files, also after a reindex, while a root
//     plugin.json that does not conform keeps its earlier answers.
//   - TestAccAgentPluginMarketplaceFilesAreStoredFiles: JFrog CLI's AQL
//     search, the storage API and the folder listings show each marketplace
//     file, with the size and checksums of the bytes its download serves,
//     to every caller who may read its path, and to no other (issue 1292).
//   - TestAccAgentPluginMarketplaceFolderIsServedAsAFolder: a folder at a
//     marketplace name answers as any folder at the item route and 404 at
//     the API path, and the other files are still stored.
//
// # References
//
//   - Research 33: docs/research/33-package-formats.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P9b, which also moved the tests here from the server root (issue
// 673).
package agentplugins
