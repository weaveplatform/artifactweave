// Package token issues, refreshes, verifies and revokes Access tokens
// (decision 0005): RS256 JWTs signed with a per-instance key, with the
// reference and refresh tokens that go with them, and the short-lived
// registry tokens of the Docker token flow. Only digests of tokens are
// stored; the store record (token_id, subject, scope, expiry) is what
// revocation removes.
//
// # Role
//
// access module, core layer: the token service. The access module's binding of
// the access/api Tokens, the token API handlers in access/adapters/http and
// the OIDC exchange call it, and the authenticator verifies tokens through it
// as its authn.TokenVerifier; other modules reach it only through access/api.
// The package imports the standard library, the kernel, platform/instance,
// platform/settings and platform/tx, and the module's api, core/authn
// (ErrInvalidToken and the API restriction check), core/model and core/port.
//
// # Inputs
//
//   - New: a Store, which the metadata store satisfies, and Options (the
//     clock and the RSA key size). The instance identity must exist.
//   - Issue: a Request; IssueRegistry: a RegistryRequest.
//   - Refresh and RefreshAs: a refresh token, and for RefreshAs the user
//     and token it must belong to (RefreshCheck).
//   - Verify and VerifyExpired: a token as a client presents it.
//   - Revoke: a token id; RevokeValue: a token as a client presents it.
//   - EncryptedPassword: a user name. Rotate: nothing.
//
// # Outputs
//
//   - Issued: the stored record, the token's secrets and its lifetime in
//     seconds. Only an encrypted password's reference token can be shown
//     again (EncryptedPassword); the other secrets exist only in the reply.
//   - model.Token, the record a token verifies to, or an error wrapping
//     authn.ErrInvalidToken.
//   - ErrInvalidRequest, ErrNeverExpiresAdminOnly and ErrRefreshOtherUser
//     for a refused request, and accessapi.ErrNotRevocable for a token too
//     short-lived to revoke.
//   - Signing keys and token records in the store; Issuer, the iss of this
//     instance's tokens.
//
// # Invariants
//
//   - A token stored without a purpose leaves only digests of its secrets
//     in the store; an encrypted password's reference token is kept sealed
//     so that it can be shown again.
//   - A token verifies only while its record exists, so a revoked token
//     fails at once. A registry token has no record: its short life, its
//     grants, its parent token and its user's current permissions bound it.
//   - A refresh revokes the token it replaces in the transaction that
//     issues the new one.
//   - After Rotate, the keys it replaces keep verifying until the latest
//     expiry of the tokens stored then, so no token is refused early.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (signing keys and an encrypted password's reference token are sealed
//     at rest).
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md
//     (release-bundle signing keys share the key store).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Names the migration introduces", which lists Store).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 278 (core/token), line 539 (its files) and line 989 (the package
//     mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1064
//
// # Status
//
// Implemented P4: access tokens, reference and refresh tokens, revocation
// and key rotation (pull request 95), refresh with an expired access token
// and the encrypted password (pull request 108), and OIDC exchanged
// identities (pull request 145). Registry tokens came with the Docker
// registry (pull requests 246 and 305), and later changes aligned expiry,
// revocation and transient users with 7.x (pull requests 449 and 486). API
// restrictions on issuance and refresh came with retry safety and bounded
// API execution (pull request 905). Moved here in P9d, where New takes Store.
//
// # Testing
//
//   - token_test.go: TestUnitIssueAndVerify, TestUnitIssueRefuses,
//     TestUnitTamperedTokens, TestUnitExpiryAndTouch, TestUnitRevoke,
//     TestUnitRefresh, TestUnitRestrictionPreservesRefreshCredential,
//     TestUnitNotAfterCapsTheLifetime, TestUnitRefreshKeepsIdentity,
//     TestUnitVerifyExpired, TestUnitRefreshAs, TestUnitEncryptedPassword,
//     TestUnitEncryptedPasswordRace, TestUnitRotation, TestUnitSharedStore
//     (a second service on the same store reuses the key and verifies the
//     first one's tokens), TestUnitNewNeedsIdentity and
//     TestUnitReferenceForm; TestUnitCLIDecodesThePayload and
//     TestUnitCLISafeProperty read a token as the JFrog CLI does.
//   - registry_test.go: TestUnitIssueRegistry, TestUnitIssueRegistryRefuses,
//     TestUnitRegistryRecordRefuses,
//     TestUnitIssueRegistryKeepsScopeAndIdentity,
//     TestUnitRegistryTokenEndsWithItsParent and
//     TestUnitRegistryTokenParentUnreadable.
//   - fault_test.go: TestUnitStoreFailures, TestUnitBadStoredKeys and
//     TestUnitRecordMismatches.
//   - accessapi_test.go: compiles only while Request and RegistryRequest
//     convert to and from their access/api counterparts.
//
// The tests run on the metadata store's in-memory store.
package token
