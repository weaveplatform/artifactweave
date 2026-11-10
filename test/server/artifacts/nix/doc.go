// Package nix runs the Nix remote routes of the assembled application: the
// native binary cache routes of a remote repository against the generic
// and cache routes that must not bypass their validation.
//
// # Role
//
// Test support: server-assembly suites for the Nix ecosystem, outside the
// modules. They drive the production router over the application runtime
// through HTTP requests. The package is one of its own so that CI selection
// can run it alone; its owner in tools/citestselection/test_ownership.json
// is nix.
//
// # Inputs
//
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission.
//   - The binary cache fixtures in internal/artifacts/ecosystem/nix/testdata,
//     read root-relative through repopath.
//
// # Outputs
//
// Assertions only:
//   - nix_remote_test.go: for a cached and a transient Nix remote, the raw
//     and cache routes refuse what only the native routes may serve, a
//     corrupt archive is not cached, a redirected archive arrives unchanged
//     and a zap refreshes the narinfo.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/nix.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1096
//
// # Status
//
// Implemented P9c. P9d moved the tests here from internal/app (TST-07).
package nix
