// Package harness builds the production router for the server-assembly
// suites under test/server, and a seeded world of users, permissions and
// repositories behind it.
//
// # Role
//
// Test support, outside the modules, for the server-assembly suites under
// test/server. It is the one place those suites build the router from, so
// the change that replaces the router (WEB-22) edits this package and no
// suite. Production code never imports it.
//
// # Inputs
//
//   - Config values from the suites, and Tune functions that adjust one
//     before the router is built.
//   - The router and its configuration from internal/server; the seeding,
//     password hashes and request helpers of test/server/support; an
//     in-memory store and a file-system blob store.
//   - Root-relative fixture paths, which RepoFile resolves through
//     platform/testkit/repopath.
//
// # Outputs
//
//   - Router: the production http.Handler built from a Config.
//   - Mirrors, MirrorStatus, SearchLimits and DefaultRedirectTTL: the types
//     and the default of the Config fields that suites name, as aliases of
//     the router's own, so that a suite never imports internal/server.
//   - ItemWorld, from NewItemWorld: the router over a store seeded with the
//     users reader, deployer and user, the deploy-libs permission, anonymous
//     read of every remote repository when asked for (AnonymousRemotes) and
//     eight local, remote and virtual repositories, and a blob store that
//     signs storage URLs (StorageSigner, SigningBlobs). Do, Item and
//     WantStatus send requests to it and check what they left behind.
//   - RepoFile: the absolute path of a root-relative fixture.
//
// # Invariants
//
//   - test/server/support never imports this package. The in-package tests
//     of internal/server import support, and this package imports
//     internal/server, so the import would close a cycle.
//   - NewItemWorld builds the same world as newItemWorld, the in-package
//     fixture of the internal/server tests, which keep their own copy because
//     they cannot import this package. A change to one is made to the other.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": test/server/support/harness, a package
//     the target document does not name; "Names the migration introduces").
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (server-assembly suites run against the production router).
//   - docs/testing/server-test-packages.md: how the suites under test/server
//     are laid out and what they share.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - https://github.com/weaveplatform/artifactweave/issues/997
//
// # Status
//
// Implemented P9d (issue 997).
//
// # Testing
//
// The package has no tests of its own: the suites under test/server exercise
// it. .testcoverage.yml leaves it out of the coverage gate, and the production
// handlers it builds stay in the gate.
package harness
