// Package gitlfs runs the Git LFS routes of the assembled server through a
// virtual repository over a remote member and a nested virtual member.
//
// # Role
//
// Test support: server-assembly suites for the Git LFS ecosystem, outside
// the modules. They drive the production router through HTTP requests. The
// package is one of its own so that CI selection can run it alone; its owner
// in tools/citestselection/test_ownership.json is gitlfs.
//
// # Inputs
//
//   - An application runtime from test/server/support/harness (harness.App)
//     on SQLite and on PostgreSQL, with the router over its store, blob
//     tiers, authenticator and remote sources (gitlfs_virtual_test.go,
//     newLFSWorld).
//   - A fake upstream (internal/artifacts/adapters/upstream/fakeupstream)
//     that answers the Git LFS batch API and serves its objects, behind
//     the remote member.
//
// # Outputs
//
// Assertions only:
//   - gitlfs_virtual_test.go: a virtual repository over [remote, nested
//     virtual -> local] serves an object only its nested local member holds
//     and one only its remote member's upstream holds, past a cycle; takes
//     an upload from a user with Deploy on its default deployment
//     repository alone; and answers 7.x's 405 without one (#1540).
//
// # References
//
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md.
//   - docs/implementation/git-lfs-client.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1540
//
// # Status
//
// Implemented P10 (ADP-07, #1540).
package gitlfs
