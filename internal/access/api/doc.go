// Package api is the access module's published surface: the permission
// vocabulary, signing key pairs and certificates, the subject and repository
// that a permission question names, the helpers that recognise a token by its
// shape, the contracts through which other modules ask access to verify
// credentials, evaluate and decide permissions, issue and revoke tokens, and
// read keys, users, API restrictions, projects, permission grants and the
// anonymous access setting, and the context accessors that carry a request's
// principal together with its permission decider.
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
// added to it, Authenticator rechecks credentials as well as verifying them,
// and Authorizer evaluates a principal once for a request and a subject that
// no request authenticated, besides the target layout's For. The decider
// travels in the request's context, bound to the principal's entry, rather
// than in the principal (decision 0044, "Composition and HTTP"), so
// kernel.Principal stays a plain value.
//
// # Inputs
//
// None: the package holds values, pure functions, interfaces and the context
// accessors, which read and write only the context they are given. The
// access module implements the interfaces: the evaluator's *Decider in
// access/core/authz satisfies Decider, and its AsAPI and FromAPI convert
// between the two, AsAPI never returning a typed nil; the module root,
// package access, binds every contract into the API that its New returns,
// and accessapitest below this package fakes each contract for other
// modules' tests.
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
//   - The user name rule: FoldUsername is the lower-case spelling a user
//     name is stored and matched in, and SameUsername compares two names
//     by it, so a module that checks a name a client sent against the
//     request's principal matches it as the authenticator did.
//   - Authenticator: verifying a password or a token, the anonymous access
//     switch, and rechecking a verified credential against current state
//     (Current, CurrentRegistry and RestoreDownload).
//   - Decider: the permission questions about one request's subject.
//   - Basic and Bearer credentials as 7.x reads them: BasicCredentials
//     decodes an Authorization value, IgnoredAuthorization reports the
//     Basic values that carry no credential at all, BearerToken reads the
//     token of a Bearer value as the props filter and RequestUtils read
//     it, and Blank is Java's isBlank, which 7.x applies to those values.
//   - API keys as 7.x reads them: APIKey is the key a request sends,
//     X-JFrog-Art-Api unless it is blank and otherwise X-Api-Key whenever
//     it is sent, which authenticates the request whatever its
//     Authorization header says; APIKeySent reports a key header that is
//     not blank, for which a refusal answers 403.
//   - Authorizer: evaluating a principal's permissions once for a request
//     (Authorize), for a principal the request did not authorise (For), and
//     for a subject no request authenticated (Decide). SubjectOf is the
//     subject an evaluation starts from for a principal.
//   - The request's caller: WithCaller stores a principal and its decider in
//     a context, WithDecider binds a decider to the principal a context
//     already carries, and DeciderFrom reads it back.
//   - Tokens, with TokenRequest, RegistryTokenRequest and IssuedToken:
//     issuing, revoking and clearing access tokens, and a user's encrypted
//     password. DefaultRefreshGrace and RegistryTokenExpiry are the token
//     lifetimes other modules apply: how long a token record outlives its
//     expiry, and a registry token's lifetime.
//   - Keys: reading key pairs and certificates.
//   - Identity, with UserInfo and its Enabled rule: reading user accounts
//     and the groups new users join.
//   - Restrictions: checking a user's temporary API restriction.
//   - Projects: reading project environments, membership and existence.
//     EnvironmentDev, EnvironmentProd and BuiltinEnvironments are the
//     built-in environments, which are not stored.
//   - Grants, with PathPair and ErrScanLimit: who holds what on an item,
//     and whether moving content changes who may reach it.
//   - AnonymousAccess: reading and switching the anonymous access setting.
//   - Settings: the access options other modules read.
//   - API: one implementation of each contract, as the module publishes
//     them, with the Settings.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "unknown permission action" and "token: token is not
//     revocable".
//   - Actions marshals to the list of its Access names, such as
//     ["READ","WRITE"], which stored permission documents hold; the JSON
//     stays byte-identical.
//   - Keypair and Certificate never serialise a private key or passphrase
//     (json:"-"); the stores seal them at rest (decision 0017).
//   - A Decider answers exactly as the access module's evaluator does, and a
//     token scope only removes actions (decision 0006).
//   - A decider answers only for the WithPrincipal call it was bound to
//     (kernel.PrincipalBinding), never for a principal that another call
//     stored, even one of equal value: a context derived with a later
//     kernel.WithPrincipal sees no decider, and one derived in any other
//     way sees the same one. Comparing bindings is safe because
//     PrincipalBinding returns the pointer that WithPrincipal stored.
//   - WithDecider never stores a typed nil: a nil decider, or one holding a
//     nil pointer, is stored as none, so DeciderFrom never returns a decider
//     that is nil inside. access/core/authn's WithAuthorized and
//     DeciderFrom use these same accessors, so a decider stored through
//     either package is visible through both.
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
//     "Public types and published api surface", "Composition and HTTP" (the
//     per-request decider bound through kernel.PrincipalBinding) and "Names
//     the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 258 to 272, and "Access and identity",
//     lines 529 to 574 ("Authorizer.For ... once per operation" at line
//     568).
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (reference tokens, tokens sent as a Basic password, revocable tokens,
//     registry tokens, and the 401 and 403 split).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission model, its three wire forms and token scopes; "Once
//     per request", line 169: the middleware's single evaluation).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     (Current refreshes the credential a replay rechecks, and a restricted
//     user's rechecks answer 403).
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Permissions v2", "Access tokens v1" and "Reference tokens", and its
//     note on user name case (#1432).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ACC (https://github.com/weaveplatform/artifactweave/issues/940), and
//     #p4-identity-and-authorization.
//   - https://github.com/weaveplatform/artifactweave/issues/963
//
// # Status
//
// Implemented P4 to P9c and moved here in P9d: the vocabulary, Keypair,
// Certificate, Subject, Repo, the token shapes and SubjectOf. Added in P9d:
// the contracts Authenticator, Authorizer, Decider, Tokens, Keys, Identity,
// Restrictions, Projects, Grants and AnonymousAccess, the Settings value, the
// API aggregate, and the context accessors WithCaller, WithDecider and
// DeciderFrom. Added by #1432: FoldUsername and SameUsername, the user name
// rule that access/core/model applies. The target layout's hooks.go, with the token gate (P17), and
// events.go, with the access events (P11), are not created yet.
//
// # Testing
//
//   - TestUnitActionsSpellings, TestUnitActionsJSON: the three spellings of
//     every action, case-insensitive Access names, ErrUnknownAction, and the
//     JSON form.
//   - TestUnitIsReferenceToken, TestUnitLooksLikeToken: the reference-token
//     form and how a Basic password is told apart from a token.
//   - TestUnitUserInfoEnabled: which account statuses may act.
//   - TestUnitFoldUsername: user names fold to lower case and match in any
//     case, as 7.x matches them.
//   - TestUnitSubjectOf: the subject of a password, a token, a token with
//     an empty scope, a groups-scoped token and the anonymous principal, as
//     the evaluator has always started from it.
//   - TestUnitDeciderContext: a decider answers for the WithPrincipal call
//     it was bound to, through derived contexts and never after a later
//     WithPrincipal; a nil or typed-nil decider hides an outer one.
//     access/core/authn's TestUnitDeciderSharedWithAccessAPI checks that its
//     accessors share the context key.
//   - TestUnitBasicCredentials, TestUnitBearerToken, TestUnitBlank: which
//     Authorization values 7.x reads as Basic or Bearer credentials, which
//     it reads no credential from, blank as Java's isBlank tells it, and
//     the user, secret or token it reads.
//   - TestUnitAPIKey: which key header is read, the first line of it, a
//     blank X-JFrog-Art-Api giving way to X-Api-Key, an X-Api-Key sent
//     empty read as a key, and which requests sent a key that is not blank.
package api
