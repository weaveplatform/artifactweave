// Package accessapitest provides hand-written fakes of every access/api
// contract, for tests that need access's answers without a store or the
// access module: a configurable Decider, Authenticator and Authorizer, a
// Tokens that records its calls, map-backed Keys, Identity, Projects and
// AnonymousAccess, and scripted Grants and Restrictions.
//
// # Role
//
// access module, test support for the api layer. It imports only access/api,
// kernel and the standard library, as an api package does (decision 0044,
// dependency rule 5), so a module's tests reach access through it without
// importing access internals, which any test may (rule 1). A test that needs
// access's real answers over a store builds the module with access.New
// instead, or the access test kit once it exists
// (https://github.com/weaveplatform/artifactweave/issues/1007).
//
// # Inputs
//
// The answers a test puts in each fake's fields: values, maps, and functions
// with the signature of the method they answer.
//
// # Outputs
//
//   - Decider, Authenticator and Authorizer: configurable. Authorizer
//     evaluates through DecideFunc and returns a Decider, by default an
//     empty one.
//   - Tokens, with TokenCalls: records the arguments of every call, and by
//     default issues numbered tokens.
//   - Keys, Identity, Projects, with Project, and AnonymousAccess:
//     map-backed.
//   - Grants and Restrictions: scripted, for a scan's outcome or a user's
//     restriction.
//
// # Invariants
//
//   - Every fake is used by pointer. A Decider value holds functions, so an
//     interface holding one would panic when compared, and Tokens and
//     AnonymousAccess hold a lock.
//   - A field left nil answers as access does when it has nothing to say: no
//     credential verifies, nothing is held, nothing changes access, no one
//     is restricted, and a record that is not there matches
//     kernel.ErrNotFound. A recheck (Authenticator.Current and
//     CurrentRegistry) returns the principal unchanged.
//   - The contract's error texts hold where a contract states them:
//     Authorizer wraps an evaluation error as "auth: permissions: %w" in
//     Authorize and "evaluate permissions: %w" in For, and returns it
//     unwrapped from Decide. The fakes' own not-found texts are not the
//     store's; a test matches them with errors.Is, never by text.
//   - A fake returns copies of the slices it holds, so a caller that changes
//     one changes nothing in the fake. Tokens and AnonymousAccess, which
//     change state, are safe for concurrent use; the other fakes only read
//     their fields.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" and "Names the migration introduces".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     "ACC tickets (access module)": consumers' tests use these fakes or
//     access.New over the in-memory store.
//   - https://github.com/weaveplatform/artifactweave/issues/966
//
// # Status
//
// Implemented P9d.
//
// # Testing
//
//   - TestUnitDecider: the zero value refuses everything, an administrator
//     is allowed what no function narrows, functions answer when set, and a
//     question for no action is refused.
//   - TestUnitAuthenticator, TestUnitAuthorizer: the defaults, the
//     functions, and Authorizer's error wraps and nil Decider on error.
//   - TestUnitTokens: the recorded calls, the numbered tokens and the
//     functions, under concurrent use.
//   - TestUnitKeys, TestUnitIdentity, TestUnitProjects,
//     TestUnitAnonymousAccess: the maps, kernel.ErrNotFound for what is not
//     there, Err, and the copies.
//   - TestUnitGrants, TestUnitRestrictions: the defaults and the scripts.
package accessapitest
