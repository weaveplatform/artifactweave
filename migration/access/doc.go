// Package access is the access module's constructor: New(Deps) binds every
// access/api contract over the store and the services app builds, and
// publishes them as one API for app to hand to the other modules.
//
// # Role
//
// access module, module root. Only app imports a module root; other modules
// reach access through access/api (decision 0044, dependency rules). Until
// access core holds the authenticator, the evaluator and the token service
// (https://github.com/weaveplatform/artifactweave/issues/1063 and
// https://github.com/weaveplatform/artifactweave/issues/1064), the bindings
// wrap the legacy internal/auth, internal/authz and internal/auth/token, so
// consumers can move onto access/api first.
//
// # Inputs
//
// Deps, which app passes New:
//
//   - Store, required: the users, groups, permissions, tokens, keys,
//     projects, settings and API restrictions the bindings read and write.
//   - Authenticator and Tokens, the authenticator and token service app
//     built over the same store; either may be nil.
//   - Settings, the access options published with the API.
//   - Now and Log, the clock and logger of the services the module builds
//     itself; it builds none yet, so New reads neither.
//
// # Outputs
//
//   - Module, whose API returns one binding of each access/api contract:
//     Authenticator, Authorizer, Tokens, Keys, Identity, Restrictions,
//     Projects, Grants and AnonymousAccess, with the Settings.
//   - KeysOf, ProjectsOf and AnonymousAccessOf: the store-only bindings, for
//     code that holds a store before any module exists, such as bootstrap.
//
// # Invariants
//
//   - New does no I/O; it refuses only Deps without a store.
//   - API().Authenticator is nil when Deps.Authenticator is, and API().Tokens
//     when Deps.Tokens is: nil interfaces, never typed nils, so a caller's
//     check for an absent service answers as its check of the legacy
//     pointer did. No binding returns a typed nil Decider either.
//   - Error texts are observable in API responses and logs, so the bindings
//     keep those of the code they replace byte for byte: Authorize's "auth:
//     permissions: %w", For's "anonymous access setting: %w" and "evaluate
//     permissions: %w", ItemGrants' "permissions with target %s: %w", and
//     the authenticator's, the token service's and the stores' own errors,
//     returned unwrapped so that errors.Is and each caller's wrap still
//     apply. Decide returns the evaluator's error unwrapped, for its caller
//     to wrap.
//   - Every binding uses its caller's context for the store, so a call inside
//     a store transaction takes part in it, and reads current state on every
//     call: nothing is cached.
//   - Grants keeps the scans' order: ItemGrants tries targets in the order
//     given and counts a permission once, for the first target whose
//     patterns admit the path; AccessChange reads permissions in name order,
//     in pages of its limit, counts each before comparing it, and refuses
//     with accessapi.ErrScanLimit past the limit.
//
// # References
//
//   - Context map, access row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns users, groups, tokens, permissions, roles, projects, environments,
//     LDAP, SAML, OIDC, sessions and SCIM; phases P4, P11, P12, P21 and P22.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 258-300, "Access and identity", lines 529-573, and
//     "Composition root", lines 746-804.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders", "Composition and HTTP" and "Names the migration
//     introduces".
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md: the token
//     binding keeps issuance and revocation rules by calling the service.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md,
//     "Once per request": Authorize is the middleware's single evaluation,
//     and For evaluates only a principal the middleware did not see.
//   - Decision 0013:
//     docs/research/decisions/0013-projects-and-environments.md: Projects
//     returns environments as stored.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md:
//     Current refreshes the credential that replays recheck, and the API
//     restriction is read on every check.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     #p4-identity-and-authorization,
//     #p11-projects-webhooks-retention-replication,
//     #p12-descriptor-export-system-configuration-ldap,
//     #p21-browser-and-enterprise-authentication,
//     #p22-scim-and-identity-lifecycle.
//   - https://github.com/weaveplatform/artifactweave/issues/966
//
// # Status
//
// Implemented P9d: New, Module and the store-only bindings, binding every
// access/api contract over the legacy authenticator, evaluator, token service
// and store. The module's routes and background work (target layout, line
// 533) stay in the server and app for now.
//
// # Testing
//
//   - TestUnitNew: a store is required and New calls none of its methods;
//     without the services their fields are nil interfaces and every other
//     contract is bound, and Settings is published.
//   - TestUnitAuthenticatorBinding, TestUnitAuthorizerBinding,
//     TestUnitTokensBinding: each method over the in-memory store and the
//     legacy services, the rechecks reading the module's token records, and
//     a call inside a transaction taking part in it.
//   - TestUnitAuthorizerErrors: Authorize's, For's and Decide's error
//     texts, each with a nil Decider.
//   - TestUnitStoreBindings: Keys, Identity, Projects, AnonymousAccess and
//     Restrictions, through API and the store-only constructors, return the
//     store's answers and its errors as it gave them.
//   - TestUnitItemGrants, TestUnitAccessChange: the scans' target order and
//     paging, their error texts, and AccessChange at exactly its limit and
//     one past it.
package access
