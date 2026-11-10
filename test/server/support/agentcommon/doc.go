// Package agentcommon provides a production-router fixture for Agent Packages
// and Agent Plugins server-route suites.
//
// # Role
//
// Test support, outside the modules, for test/server/artifacts/agentpackages
// and test/server/artifacts/agentplugins.
//
// # Inputs
//
//   - The production router from test/server/support/harness (app.NewRouter),
//     an in-memory store and a file-system blob store, and the seeding of
//     test/server/support.
//
// # Outputs
//
//   - World, from NewWorld: the router with the users deployer and user and
//     the local repository libs-local.
//   - Assertions the two suites share: WantStatus, HitsOf and SetTrashcan.
//
// # References
//
//   - Research 33: docs/research/33-package-formats.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/673
//
// # Status
//
// Implemented P9b (issue 673).
package agentcommon
