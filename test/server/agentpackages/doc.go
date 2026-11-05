// Package agentpackages runs the Agent Packages routes of the assembled
// server: the APM protocol and the package lifecycle.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// agentpackages.
//
// # Inputs
//
//   - The world of test/server/agentcommon.
//
// # Outputs
//
//   - TestAccAgentPackagesAPMProtocolAndLifecycle: APM publish, version
//     listing, download and search in local and federated repositories, with
//     the permission and size limits, and deletion with the trash can on.
//
// # References
//
//   - Research 33: docs/research/33-package-formats.md.
//   - docs/operations/agent-packages.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P9b, which also moved the tests here from internal/server
// (issue 673).
package agentpackages
