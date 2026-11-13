// Package kittest builds the item kit over a seeded world, for unit tests of
// the kit and of the handlers that embed it, without a router or the access
// module.
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
//   - The metadata store New seeds, fresh and empty: for most tests the
//     in-memory composite, which a test file may import and this package,
//     whose files are not test files, may not (dependency rule 1).
//   - The Deps a test builds the kit with; World.Kit fills in what they
//     leave nil.
//
// # Outputs
//
//   - World: the store, kept with its own type and seeded with a generic
//     repository of each class (Local, Remote, Federated, and Virtual over
//     Local and Remote, deploying to Local), and an empty blob store on
//     disk.
//   - World.Put: a file stored as a deploy leaves it, blob and item.
//   - World.Kit: an itemkit.Kit over the world.
//   - Admin, AdminAccess and Request: an administrator, its access, and a
//     request carrying both as the authentication middleware leaves them.
//   - Send and WantDownloadConditionals: the check, for a package route's
//     tests, that a document the route builds answers conditional requests
//     and ranges as the download answers a file (itemkit.ServeContent).
//
// # Invariants
//
//   - Each New has its own blob directory and seeds only the store it is
//     given, so tests that build their own worlds over their own stores may
//     run in parallel.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P9d (WEB-06), with the store from the caller (STO-19).
//
// # Testing
//
// It has no tests of its own: the item kit's tests (itemkit's kit_test.go)
// build every world they use with it, and the package routes' conditional
// tests call WantDownloadConditionals. Its remaining branches are t.Fatal paths
// taken only when the store or the blob directory fails, so it is excluded
// from coverage (.testcoverage.yml) like the other fixtures.
package kittest
