// Package idtoken verifies ID tokens that an external OpenID Connect
// provider, typically a CI system, signed: the subject tokens of the Access
// OIDC token exchange (POST /access/api/v1/oidc/token, research 21). Signing
// keys come from the provider's discovery document and JWKS, cached per
// provider.
//
// # Role
//
// access module, core layer: the ID token verifier. core/oidc verifies each
// exchange's subject token through it, and the access module's HTTP adapter
// builds the one Verifier the exchanges share. It is not merged into
// core/oidc, where the target layout puts it, because both packages declare
// Provider, and a merge would rename one of them (decision 0044). The
// package imports the standard library and go-jose.
//
// # Inputs
//
//   - New: Options, the HTTP clients with and without the environment's
//     proxy, the key cache lifetime and the clock.
//   - Verify: a token as the client sent it and the Provider it must agree
//     with: the issuer URL, the expected issuer and audience, an Azure
//     application id and whether to fetch through the proxy.
//
// # Outputs
//
//   - The token's claims, once its signature, issuer, audience and validity
//     verify.
//   - ErrInvalid for a token that does not verify, and ErrKeys for a
//     provider whose keys cannot be fetched.
//
// # Invariants
//
//   - Only asymmetric algorithms verify: a token signed with HS* or none is
//     refused.
//   - A token naming a key not in the cache fetches the keys again at most
//     once every 30 seconds, so forged key ids cannot make the server
//     hammer the provider.
//
// # References
//
//   - Research 21: docs/research/21-oidc-token-exchange.md.
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md,
//     lines 22 to 25.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": idtoken keeps its name and package).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 283 and 987, which merge it into core/oidc; decision 0044
//     records the difference.
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1073
//
// # Status
//
// Implemented P4 with the OIDC token exchange (pull request 145). Moved here
// in P9d.
//
// # Testing
//
// idtoken_test.go: TestAccVerify, TestAccVerifyRefuses, TestAccKeyRotation,
// TestAccKeysUnavailable, TestAccAzureAppID, TestAccProxyChoice,
// TestAccBadDocuments and TestAccKeyWithoutID, each against a running
// idtokentest provider.
package idtoken
