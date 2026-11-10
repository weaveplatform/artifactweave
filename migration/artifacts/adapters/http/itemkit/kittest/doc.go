// Package kittest builds the item kit over a seeded in-memory world, for
// unit tests of the kit and of the handlers that embed it, without a router
// or the access module.
//
// # Role
//
// artifacts module, HTTP adapter layer: test support for the item kit,
// artifacts/adapters/http/itemkit. Only tests import it. It reaches access
// only through access/api and that package's fakes, access/api/accessapitest,
// as the kit itself does.
//
// # Inputs
//
//   - The test, for its temporary directory and its failures.
//   - The Deps a test builds the kit with; World.Kit fills in what they
//     leave nil.
//
// # Outputs
//
//   - World: an in-memory store seeded with a generic repository of each
//     class (Local, Remote, Federated, and Virtual over Local and Remote,
//     deploying to Local), and an empty blob store on disk.
//   - World.Put: a file stored as a deploy leaves it, blob and item.
//   - World.Kit: an itemkit.Kit over the world.
//   - Admin, AdminAccess and Request: an administrator, its access, and a
//     request carrying both as the authentication middleware leaves them.
//
// # Invariants
//
//   - Each New has its own store and blob directory, so tests that build
//     their own worlds may run in parallel.
//   - The world holds no permissions. A test grants through the decider it
//     puts in a request's context (Request, or accessapi.WithCaller with an
//     accessapitest.Decider), and World.Kit's authorizer fake allows nothing
//     to a request that carries none.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": the test files of any module may import test
//     support).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (unit tests without a router).
//   - https://github.com/weaveplatform/artifactweave/issues/1075
//
// # Status
//
// Implemented P9d (WEB-06).
//
// # Testing
//
// It has no tests of its own: the item kit's tests (itemkit's kit_test.go)
// build every world they use with it. Its remaining branches are t.Fatal
// paths taken only when the in-memory store or the blob directory fails,
// so it is excluded from coverage (.testcoverage.yml) like the other
// fixtures.
package kittest
