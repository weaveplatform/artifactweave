// Package p2 runs the P2 routes of the assembled server: remote and virtual
// repositories, archive suffixes, external composite children and their
// origins, and the cache's integrity.
//
// # Role
//
// Test support: server-assembly suites for the P2 ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is p2.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The Eclipse GLSP update site files in
//     internal/artifacts/ecosystem/p2/testdata, read root-relative through
//     repopath, and remote sources over stub backends (sourceIdentity,
//     sourceBackend).
//
// # Outputs
//
// Assertions only:
//   - p2_test.go: archive suffix bounds and permissions, the remote and
//     virtual wire paths, invalid or changed cached representations and
//     virtual member filtering.
//   - p2_external_test.go, p2_external_error_test.go and
//     p2_external_cache_integrity_test.go: external composite children,
//     their depth, origins and authority, metadata fallback, archive
//     corruption, forged member selection and a changed published checksum.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/p2.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068 and
//     https://github.com/weaveplatform/artifactweave/issues/1084
//
// # Status
//
// Implemented P9c (the plan's P31 group). P9d moved the router-level tests
// here from the server root (TST-06), and the tests that call the protocol's
// unexported code moved with it to internal/artifacts/ecosystem/p2/http
// (WEB-15).
package p2
