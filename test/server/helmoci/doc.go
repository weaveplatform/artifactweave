// Package helmoci replays against the assembled server the requests that jf
// helm push and jf rt bp send after helm pushes a chart to an OCI repository.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// helmoci.
//
// # Inputs
//
//   - The world of test/server/support/registryfixture and the registry
//     fixtures of test/server/support.
//
// # Outputs
//
//   - TestAccJFHelmPushRequests: the AQL over the version folder finds
//     manifest.json and the sha256__ blobs, the recursive property edit marks
//     the folder and its files with the build, the manifest downloads and the
//     build-info's helm module names the chart. The acceptance feature
//     helm_oci.feature runs the pinned jf and helm themselves.
//
// # References
//
//   - Decision 0039: docs/research/decisions/0039-helm.md.
//   - Research 30: docs/research/30-helm.md, section 1.3.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P8; P9b moved the tests here from internal/server (issue 673).
package helmoci
