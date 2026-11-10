// Package authn authenticates requests and seeds the built-in principals
// (decision 0005): a user name and password, an access or reference token
// and a registry token each become a kernel.Principal, and a principal kept
// for work that outlives its request is rechecked against current state
// before that work acts for it. Passwords are Argon2id hashes in PHC string
// form, so the cost parameters travel with each hash and can be raised
// without breaking the hashes already stored.
//
// # Role
//
// access module, core layer: the authenticator. The request middleware in
// access/adapters/http and the access module's binding of the access/api
// Authenticator call it; other modules reach it only through access/api.
// It also evaluates a request's permissions once (Authorize), carries the
// principal and its decider in the context (WithAuthorized, DeciderFrom),
// and seeds the users, group, permissions and build-info repository a
// fresh instance starts with. The package imports the standard library,
// golang.org/x/crypto/argon2, the kernel, platform/settings, platform/tx
// (the seed's transaction) and platform/httpkit/apierror, the module's api,
// core/authz, core/model and core/port, and artifacts/api for the
// Repositories through which the seed creates the build-info repository.
//
// # Inputs
//
//   - NewAuthenticator: the user store, the settings store (the anonymous
//     access switch, basic authentication, the lock and expiry policies and
//     the Access lock settings that suspension follows) and Options: the
//     verified-password cache, the hash cost and concurrency, the
//     TokenVerifier, the group store that a transient user's automatic
//     groups come from, and the API restriction store.
//   - Basic, Token and ExpiredToken: the credentials a request presents.
//   - Current, CurrentRegistry and RestoreDownload: a principal verified
//     earlier, or the reference a download envelope carries, with the token
//     store.
//   - Authorize: authz.Sources and the principal.
//   - Seed and SeedHashed: a SeedStore (the users, groups, permissions,
//     settings and transaction the seed writes in), the artifacts module's
//     Repositories over the same unit of work, the admin password or its
//     hash, and the time. Seeded needs only the settings.
//
// # Outputs
//
//   - A kernel.Principal, or one of the kernel's credential sentinels
//     (ErrBadCredentials, ErrAccountLocked, ErrPasswordExpired and
//     ErrAPIRestricted) or the package's own (ErrBasicAuthDisabled,
//     ErrLoginSuspended, ErrHashFormat and ErrInvalidToken).
//   - From Authorize, the principal as the evaluator decides it with its
//     *authz.Decider, which WithAuthorized stores in a context and
//     DeciderFrom reads back.
//   - Password hashes (HashPassword, HashPasswordWith) and their check
//     (CheckPassword), and the stored policies with their defaults.
//   - The built-in principals, the build-info repository and the seed
//     marker, written in one transaction (Seed).
//
// # Invariants
//
//   - A credential reference never authenticates a new request: Current,
//     CurrentRegistry and RestoreDownload only recheck a credential that
//     Basic or Token verified, against the user, token, policy and
//     anonymous access state now, and never keep, forward or verify a
//     password or token again.
//   - An unknown user costs the same hash as a known one, so the time a
//     wrong answer takes does not tell which user names exist.
//   - The seed writes everything or nothing, and an instance that loses
//     the race to seed a shared database reports false, not an error.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md
//     (Authorize and the decider in the context).
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md
//     (local users and tokens first; the other tiers come later).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Research: docs/research/05-platform-identity-and-security-apis.md (the
//     built-in principals) and docs/research/21-oidc-token-exchange.md
//     (transient users).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 277 (core/authn), lines 537 and 538 (its files) and line 986
//     (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1064
//
// # Status
//
// Implemented P4: Basic authentication and the seeded principals (pull
// request 56), token authentication (pull request 95), the lock and expiry
// policies (pull request 97), a request's permissions evaluated once (pull
// request 101), the basic authentication switch (pull request 105), refresh
// with an expired access token (pull request 108) and the identities of OIDC
// exchanged tokens (pull request 145). Registry tokens came with the Docker
// registry (pull requests 246 and 305), and later changes aligned token
// expiry, transient users and the admin scope with 7.x (pull request 449)
// and added login suspension (pull request 470). P9 added the rechecks for
// work that outlives its request: Current (pull request 648),
// CurrentRegistry (pull request 664) and RestoreDownload (pull request 674);
// API restrictions came with retry safety and bounded API execution (pull
// request 905). Staged in P9d, where the seed creates the build-info
// repository through the artifacts module's Repositories.
//
// # Testing
//
//   - auth_test.go: TestMain lowers the hash cost for the package's tests;
//     TestUnitHashAndCheck, TestUnitProductionHashParams,
//     TestUnitHashParamsReachTheHashAndTheDummy,
//     TestUnitCheckPasswordRejectsMalformedHashes, TestUnitRequire,
//     TestUnitBasic, TestUnitBasicErrors, TestUnitAnonymousAllowed and
//     TestUnitCacheBound.
//   - token_test.go: TestUnitTokenPrincipal, TestUnitExpiredToken,
//     TestUnitEncryptedPasswordToken, TestUnitOIDCTokenIdentities,
//     TestUnitTransientTokenScopes and TestUnitRegistryTokenPrincipal.
//   - policy_test.go: TestUnitPasswordExpired, TestUnitLockoutThroughBasic,
//     TestUnitLockBeforeSuspensionAndPassword, TestUnitExpiryThroughBasic
//     and TestUnitPolicyReadErrors.
//   - suspension_test.go: TestUnitSuspensionDelay,
//     TestUnitSuspendedForMessage, TestUnitSuspensionThroughBasic,
//     TestUnitSuspensionBeforeHashing, TestUnitSuspensionSettings,
//     TestUnitSuspensionsBounded and TestUnitSuspensionsForgetStale.
//   - current_test.go, current_registry_test.go and download_test.go:
//     TestUnitCurrentPrincipalRechecksIdentity,
//     TestUnitCurrentRegistryKeepsGrantAndRechecksCredential,
//     TestUnitDownloadCredentialRestoration and
//     TestUnitDownloadCredentialRejectsUnverifiedAndMixedReferences.
//   - api_restriction_test.go: TestUnitAPIRestrictionCredentialsAndRecovery.
//   - authorize_test.go and principal_test.go: TestUnitAuthorize,
//     TestUnitDeciderFrom, TestUnitWithAuthorized and
//     TestUnitDeciderSharedWithAccessAPI (one context key for this package
//     and access/api).
//   - seed_test.go: TestUnitSeedInMemory, TestAccSeedSQLite,
//     TestAccSeedPostgres, TestUnitSeedLosesARace and TestUnitSeedErrors,
//     with a stand-in for the artifacts Repositories (internal/app's seed
//     tests check the real one). The two acceptance tests seed the
//     metadata store's SQLite and PostgreSQL stores; the PostgreSQL test
//     needs Docker and skips without it.
//
// The other tests run on the metadata store's in-memory store.
package authn
