// Package vcs runs the VCS routes of the assembled server: the REST contract
// and GitLab subgroups, the STASH, OLDSTASH and Artifactory peer providers,
// and file delivery.
//
// # Role
//
// Test support: server-assembly suites for the VCS ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is vcs.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - Remote sources over stub backends (sourceIdentity, sourceBackend) and
//     the archives in migration/artifacts/ecosystem/vcs/http/testdata, read
//     root-relative through repopath; vcsTableCases, a copy of the root's
//     route table.
//   - curl, when present: TestAccVCSRemoteRESTAndSubgroup sends its native
//     curl archive request with it, and fails without it when
//     ARTIFACTWEAVE_REQUIRE_CURL is set, as the quality gate's VCS step
//     does.
//
// # Outputs
//
// Assertions only:
//   - vcs_routes_test.go: the REST routes and subgroups, branch, ref, commit
//     and release downloads, denials before upstream, upstream failures on
//     every documented route, the nested grammar, unknown refs and missing
//     files, malformed upstream refs and property-constrained archives.
//   - vcs_legacy_test.go and vcs_delivery_test.go: commit resolution and
//     redirect changes, peer tag and branch bounds, rootless file precedence
//     and peer file usage.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/vcs.md, docs/research/39-vcs-legacy.md and
//     docs/client-recipes/vcs.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068 and
//     https://github.com/weaveplatform/artifactweave/issues/1085
//
// # Status
//
// Implemented P9c (the plan's P33 group). P9d moved the router-level tests
// here from internal/server (TST-06), and the tests that call the
// protocol's unexported code moved with it to
// migration/artifacts/ecosystem/vcs/http (WEB-16).
package vcs
