// Package harness builds the production router for the server-assembly
// suites under test/server, a seeded world of users, permissions and
// repositories behind it, and the application runtime that serve opens.
//
// # Role
//
// Test support, outside the modules, for the server-assembly suites under
// test/server. It is the one place those suites build the router from, so a
// change to how the router is built edits this package and no suite.
// Production code never imports it.
//
// # Inputs
//
//   - Config values from the suites, and Tune functions that adjust one
//     before the router is built.
//   - The router and its configuration from migration/app (app.NewRouter
//     and app.RouterConfig); the seeding, password hashes and request
//     helpers of test/server/support; an in-memory store and a file-system
//     blob store.
//   - The application runtime from migration/app (app.OpenRuntime) with the
//     default configuration of migration/app/config, and a fake upstream
//     from the artifacts module's fakeupstream.
//   - Root-relative fixture paths, which RepoFile resolves through
//     platform/testkit/repopath.
//
// # Outputs
//
//   - Router: the production http.Handler built from a Config.
//   - Mirrors, MirrorStatus, SearchLimits and DefaultRedirectTTL: the types
//     and the default of the Config fields that suites name, as aliases of
//     those the artifacts module's HTTP adapter declares, so that a suite
//     names them through this package alone.
//   - ItemWorld, from NewItemWorld: the router over a store seeded with the
//     users reader, deployer and user, the deploy-libs permission, anonymous
//     read of every remote repository when asked for (AnonymousRemotes) and
//     eight local, remote and virtual repositories, and a blob store that
//     signs storage URLs (StorageSigner, SigningBlobs). Do, Item and
//     WantStatus send requests to it and check what they left behind.
//   - RepoFile: the absolute path of a root-relative fixture.
//   - App: the application runtime opened as serve opens it, over AppConfig
//     (the default configuration with a temporary data directory) with
//     AppOptions (a cheap password hash and a small signing key), closed
//     when the test ends.
//   - RemoteWorld, from NewRemoteWorld: the router over an App runtime with
//     the generic remote repository remote in front of a fake upstream, the
//     users reader and denied and the remote-read permission. Request sends
//     requests to it and Update saves a changed remote.
//
// # Invariants
//
//   - test/server/support never imports this package. migration/app's
//     in-package tests import support, and this package imports
//     migration/app, so the import would close a cycle.
//   - migration/app's in-package tests never import this package: it imports
//     migration/app, so the import would close a cycle.
//   - NewItemWorld builds the same world as newItemWorld, the in-package
//     fixture of the router's guards in migration/app (fixtures_test.go),
//     which keep their own copy because they cannot import this package. A
//     change to one is made to the other.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1096 (App and
//     RemoteWorld, for the suites that left migration/app).
//   - https://github.com/weaveplatform/artifactweave/issues/1097 (the router
//     built through app.NewRouter).
//
// # Status
//
// Implemented P9d (issues 997, 1096 and 1097).
//
// # Testing
//
// The package has no tests of its own: the suites under test/server exercise
// it. .testcoverage.yml leaves it out of the coverage gate, and the production
// handlers it builds stay in the gate.
package harness
