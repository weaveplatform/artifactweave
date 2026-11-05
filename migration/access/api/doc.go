// Package api is the access module's published surface: the permission
// vocabulary, signing key pairs and certificates, the subject and repository
// that a permission question names, the helpers that recognise a token by its
// shape, and the contracts through which other modules ask access to decide
// permissions, issue and revoke tokens, and read keys, users, API
// restrictions, projects, permission grants and the anonymous access setting.
//
// # Role
//
// access module, api layer. Other modules reach access only through this
// package, and it imports only kernel and the standard library (decision
// 0044, dependency rules 5 and 6), so it declares its own types. Artifacts
// code uses the vocabulary, Keypair, Certificate, Repo and Subject heavily,
// and an api package cannot import access core, so they are declared here
// rather than in the core packages that evaluate them. The contracts are
// today's calls from other modules into access, which is wider than the
// target layout's api: Restrictions, Projects, Grants and AnonymousAccess are
// added to it.
//
// # Inputs
//
// None: the package holds values, pure functions and interfaces. The access
// module implements the interfaces, and its module root binds them into an
// API (https://github.com/weaveplatform/artifactweave/issues/966).
//
// # Outputs
//
//   - The permission vocabulary: ResourceType and its constants, the
//     special targets (TargetAny, TargetAnyLocal, TargetAnyRemote and
//     TargetAnyDistribution) with BuildInfoRepository and
//     ReleaseBundlesRepository, and Actions with its constants. AccessNames,
//     LegacyNames and Letters spell a set of actions in the three wire
//     forms; ParseAccessActions, ParseLegacyActions and ParseLetters read
//     them back and refuse an unknown name with ErrUnknownAction.
//   - Keypair, of a KeypairType, and Certificate: the signing key pairs and
//     the client certificates of remote repositories.
//   - Subject, who a request acts as, and Repo, the repository a permission
//     target names.
//   - Token shapes: IsReferenceToken and LooksLikeToken tell a token from a
//     password; ErrNotRevocable refuses to revoke a short-lived token.
//   - Decider: the permission questions about one request's subject.
//   - Tokens, with TokenRequest, RegistryTokenRequest and IssuedToken:
//     issuing, revoking and clearing access tokens, and a user's encrypted
//     password.
//   - Keys: reading key pairs and certificates.
//   - Identity, with UserInfo and its Enabled rule: reading user accounts
//     and the groups new users join.
//   - Restrictions: checking a user's temporary API restriction.
//   - Projects: reading project environments, membership and existence.
//   - Grants, with PathPair and ErrScanLimit: who holds what on an item,
//     and whether moving content changes who may reach it.
//   - AnonymousAccess: reading and switching the anonymous access setting.
//   - Settings: the access options other modules read.
//   - API: one implementation of each contract, as the module publishes
//     them.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "unknown permission action" and "token: token is not
//     revocable". Until the migration's bridges are deleted
//     (https://github.com/weaveplatform/artifactweave/issues/1101), the
//     legacy names of ErrUnknownAction and ErrNotRevocable are these same
//     values, so errors.Is matches under either name.
//   - Actions marshals to the list of its Access names, such as
//     ["READ","WRITE"], which stored permission documents hold; the JSON
//     stays byte-identical.
//   - Keypair and Certificate never serialise a private key or passphrase
//     (json:"-"); the stores seal them at rest (decision 0017).
//   - A Decider answers exactly as the access module's evaluator does, and a
//     token scope only removes actions (decision 0006).
//   - TokenRequest and RegistryTokenRequest carry every field of the token
//     service's requests, in the same order and of the same types, so the
//     module converts them and issuance keeps the rules of decision 0005.
//   - Every contract call that reads or writes a store uses the caller's
//     context, so a call inside a store transaction takes part in it, and
//     the store's sentinels, such as kernel.ErrNotFound, reach the caller
//     for errors.Is.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface", "Type-alias bridges" and
//     "Names the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 258 to 272, and "Access and identity",
//     lines 529 to 574.
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (reference tokens, tokens sent as a Basic password, revocable tokens,
//     registry tokens).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission model, its three wire forms and token scopes).
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Permissions v2", "Access tokens v1" and "Reference tokens".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ACC (https://github.com/weaveplatform/artifactweave/issues/940), and
//     #p4-identity-and-authorization.
//   - https://github.com/weaveplatform/artifactweave/issues/963
//
// # Status
//
// Implemented P4 to P9c and staged in P9d: the vocabulary, Keypair,
// Certificate, Subject, Repo and the token shapes. Added in P9d: the
// contracts Decider, Tokens, Keys, Identity, Restrictions, Projects, Grants
// and AnonymousAccess, the Settings value and the API aggregate. Still to
// come in P9d: Authenticator, Authorizer and the context accessors
// WithCaller, WithDecider and DeciderFrom
// (https://github.com/weaveplatform/artifactweave/issues/966). The target
// layout's hooks.go, with the token gate (P17), and events.go, with the
// access events (P11), are not created yet.
//
// # Testing
//
//   - TestUnitActionsSpellings, TestUnitActionsJSON: the three spellings of
//     every action, case-insensitive Access names, ErrUnknownAction, and the
//     JSON form.
//   - TestUnitIsReferenceToken, TestUnitLooksLikeToken: the reference-token
//     form and how a Basic password is told apart from a token.
//   - TestUnitUserInfoEnabled: which account statuses may act.
package api
