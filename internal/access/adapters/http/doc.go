// Package accesshttp is the access module's HTTP adapter: the authentication
// middleware, the access spec's server and the artifactory spec's security
// partial, with every access handler, the API restriction middleware and the
// access contributions to the router's request handling.
//
// # Role
//
// access module, HTTP adapter layer. The composition root mounts what this
// package exports; nothing else in the module serves HTTP. It reaches the
// artifacts module only through artifacts/api, and it imports no server or
// router package: the router hands it what it needs from other modules as
// options and functions.
//
// # Inputs
//
//   - Driving: the router, which installs Authenticate with AuthnOptions,
//     PlusAsSpace, APIRecoveryContext and Server.CheckAPIRestriction, mounts
//     Server.RegisterRoutes, and embeds Server.Access and Server.Security in
//     the generated strict servers beside the generated stubs.
//   - Deps, from the composition root: the access API, the Store (the
//     access ports, the settings and the configuration documents), the
//     authenticator, the token service, the OIDC verifier, the settings,
//     the password cost, the compatibility version, the logger and the
//     artifacts module's Repositories, through which the handlers read
//     every repository.
//   - From the router, as functions: the health predicate and limit error
//     writer of CheckAPIRestriction, and the skips, registry predicates and
//     credential keepers of AuthnOptions.
//
// # Outputs
//
//   - Authenticate and AuthnOptions: the request's principal and its
//     decider on the context (kernel.Principal, authn.WithAuthorized).
//   - Server, built by New over Deps: AccessService and SecurityAPI, the
//     partial strict servers, and RegisterRoutes for the legacy token
//     routes.
//   - The router's contributions: PublicRoutes, SlashCollections,
//     DetailRules with RequestDetails, PlusAsSpace, APIRecoveryRequest,
//     APIRecoveryContext and Server.CheckAPIRestriction.
//
// # Invariants
//
//   - Authenticate decides in the order its comment lists (decision 0005):
//     self-authenticated routes and skips, then credentials, which answer
//     401 at once when they do not verify, then anonymous access, and only
//     then the public routes. A Basic value 7.x reads no credential from,
//     such as "Basic Og==", counts as no credential at all.
//   - Every principal it lets through is authorised once, and handlers ask
//     that decider (decision 0006).
//   - A key pair delete clears the repositories' references to the pair and
//     deletes it in one store transaction: a failure at any step leaves
//     both as they were.
//   - Statuses, envelopes and error and log text are what JFrog's clients
//     and the operators' logs read, so they stay byte-identical.
//   - accessState, which both partials embed, defines no method named after
//     an operation, so the composite's depth-2 stubs never collide with it.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (credential forms, the expired token refresh, the compatibility
//     version).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the once-per-request decider and the permission APIs).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md (API
//     restrictions and the recovery requests, bounded as before).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 290 to 295 and 553 (the access module's HTTP adapter), and the
//     mapping at lines 395 and 396.
//   - https://github.com/weaveplatform/artifactweave/issues/1072
//
// # Status
//
// Implemented P4 as the server root's access handlers: the legacy security
// and Access APIs, permissions, tokens and the OIDC exchange; API
// restrictions came with retry safety and bounded API execution (pull
// request 905). P9d split them into partial strict servers (WEB-07) and
// moved them here (WEB-09) and narrowed Deps.Store to the adapter's own
// Store (ACC-14).
//
// # Testing
//
// The unit tests here call the middleware and the partials directly over
// in-memory stores, without the router; TestUnitDeleteKeyPairIsOneTransaction
// checks the key pair transaction. The route suites under test/server/access
// and the router's guards in app (its status, principal and ledger tables)
// reach the handlers through the router.
package accesshttp
