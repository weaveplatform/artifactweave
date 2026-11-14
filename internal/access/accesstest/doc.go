// Package accesstest gives other modules' tests the real access module over a
// store: the authenticator, the permission evaluator and the token service
// that a running server builds, bound through access.New as app binds them,
// so that a test authenticates and authorises without importing access
// internals.
//
// # Role
//
// access module, test support: the real access module for other modules'
// tests, and the access module's supported entry point for them (decision
// 0044). Any module's test files may import it, as test support (dependency
// rule 1). It imports its own module root under rule 7's one named
// exception, which lets a module's test kit <m>/<m>test import its own root
// <m> and nothing wider. Tests that need access's answers without a store
// use the access/api/accessapitest fakes instead.
//
// # Inputs
//
//   - A testing.TB, which New and Over fail when the module cannot be built,
//     and which Middleware reports a failed authorisation to.
//   - A Store, access.Store with the instance identity, holding the users,
//     groups, permissions, settings, tokens and API restrictions that every
//     answer reads: a fresh in-memory store from New, the access module's
//     and the platform's in-memory stores over one unit of work, or the
//     test's own through Over.
//   - The options: WithNow, WithTokens, WithTokenVerifier and WithSettings.
//
// # Outputs
//
//   - Kit.API: the module's accessapi.API, as app's module publishes it.
//   - Kit.Authorize: a context carrying an authorised principal and its
//     decider.
//   - Kit.Decide: a decider for a subject that no request authenticated.
//   - Kit.Middleware: a middleware that authorises each request's principal.
//   - Kit.Verify: the stored record of a token the kit's token service
//     issued.
//   - HashPassword and ErrLoginSuspended: cheap password hashes for the
//     users a test stores, and the suspended-login sentinel, which the
//     kernel does not declare.
//   - The access records a test stores (User, Group, Permission, Token,
//     Project, Role and the rest), the store ports a fake wraps (UserStore,
//     PermissionStore with PermissionQuery, TokenStore with TokenFilter,
//     and APIRestriction) and
//     the constants a test sets (UserDisabled, UserLocked and
//     RoleCustomGlobal): aliases of access/core/model and access/core/port,
//     which a test outside access may not import.
//
// # Invariants
//
//   - The kit builds the module as app does: the authenticator over the
//     store's users and settings, with its groups, its API restrictions and
//     the token verifier, then access.New. Only the costs differ, so tests
//     stay fast: passwords hash with 64 KiB and one pass, and signing keys
//     have 1024 bits.
//   - Authorize and Middleware store a principal and its decider together,
//     with accessapi.WithCaller, never the principal alone, so a handler
//     finds the decider the server's middleware would have stored (decision
//     0006, "Once per request").
//   - Middleware reports a failed authorisation with tb.Errorf and answers
//     500. It never calls tb.Fatal, which must not run off the goroutine
//     running the test, where handlers may run.
//   - Every answer reads the store when it is asked for, so what a test
//     stores after building the kit applies from its next call.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rule 7's named exception) and "Names the migration
//     introduces".
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md:
//     the kit's tokens are the real service's, so tests never mint them by
//     hand.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md,
//     "Once per request": the kit evaluates through the Authorizer the server
//     uses, so test outcomes match production.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md:
//     tests run against the real module over an in-memory or SQL store, not
//     a fake.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, "ACC
//     tickets (access module)".
//   - https://github.com/weaveplatform/artifactweave/issues/1007
//
// # Status
//
// Implemented P9d. Over takes the access Store with the instance identity
// (https://github.com/weaveplatform/artifactweave/issues/1073), and New
// builds over the access module's and the platform's in-memory adapters
// (https://github.com/weaveplatform/artifactweave/issues/1098). The record
// and port aliases serve the tests that named access internals
// (https://github.com/weaveplatform/artifactweave/issues/1100).
//
// # Testing
//
// A test outside access that needs an authenticator, a decider or tokens
// builds them with this kit (Over, Authorize, Middleware, Decide, API,
// Verify, HashPassword) rather than from access internals;
// TestUnitTestsAvoidAccessInternals (internal/layout) enforces it outside
// the composition root and the server suites.
//
// The kit's own tests:
//
//   - TestUnitAuthorizeAndDecideReadTheTestStore: a permission the test
//     stores allows a read through Authorize and Decide, and without it the
//     read is denied; Over keeps the test's store and settings, and
//     Authorize keeps the binding's error text.
//   - TestUnitAnonymousAccessOffGrantsNothing: with anonymous access off,
//     the anonymous principal gets nothing.
//   - TestUnitMiddleware: the principal and decider the middleware stores
//     read back through the kernel, access/api and authn.DeciderFrom; a
//     failed authorisation is reported on the test and answers 500.
//   - TestUnitWithTokens: the instance identity is created when missing and
//     kept when present, the signing key exists once the kit is built, an
//     issued token verifies, and a store that cannot give the identity or
//     the keys fails the test; without the option every token is refused.
//   - TestUnitVerify: the record of an issued token, on the kit's clock, and
//     a failed test on a kit without a token service.
//   - TestUnitWithTokenVerifier: the verifier answers the authenticator,
//     with or without the token service, and a transient user joins the
//     store's auto-join groups.
//   - TestUnitHashPassword: the cheap hash passes Basic, and the store's API
//     restrictions apply.
//   - TestUnitErrLoginSuspended: a suspended login matches
//     ErrLoginSuspended until the kit's clock passes the suspension.
package accesstest
