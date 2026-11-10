// Package cran runs the CRAN routes of the assembled server: remote and
// offline indexes, virtual members, POST uploads and the archive migration.
//
// # Role
//
// Test support: server-assembly suites for the CRAN ecosystem, outside the
// modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is cran.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The CRAN assertthat fixtures in
//     migration/artifacts/ecosystem/cran/testdata, read root-relative
//     through repopath, remote sources over stub backends (sourceIdentity,
//     sourceBackend), and archives from support.TarGz (conanTestArchive).
//
// # Outputs
//
// Assertions only:
//   - cran_test.go and cran_routes_test.go: virtual index filtering, native
//     POST and reindex, remote index faults and authority checks, offline
//     cached reads, nested virtual members, upload refusals and overwrite
//     authority, and index variants.
//   - cran_archive_test.go: the archive migration, its rollback, refusals,
//     collisions, destination policy and storage failure.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - docs/testing/cran.md.
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
// Implemented P9c (the plan's P32 group). P9d moved the router-level tests
// here from internal/server (TST-06), and the tests that call the
// protocol's unexported code moved with it to
// migration/artifacts/ecosystem/cran/http (WEB-15).
package cran
