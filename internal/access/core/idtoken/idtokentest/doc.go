// Package idtokentest is a fake OpenID Connect provider for tests: it serves
// a discovery document and a JWKS, as GitHub Actions, GitLab and other CI
// systems do, and mints ID tokens signed with its current key.
//
// # Role
//
// access module, test support for core/idtoken and core/oidc. Tests outside
// access use it too, the access route suite and the acceptance steps among
// them, because its name ends in test: the access internals guard lets
// tests import access test support. The package imports the standard
// library, chi and go-jose.
//
// # Inputs
//
//   - New: nothing; it starts the provider on a local listener.
//   - Mint: the audience, the lifetime and extra claims of a token.
//   - Rotate: nothing; it adds a signing key and keeps the old ones
//     published, as providers do while tokens they signed are alive.
//   - SignRS256: a key, its id and the claims.
//
// # Outputs
//
//   - Issuer: the running provider, with its URL, how many times its JWKS
//     was fetched (Fetches) and the appid queries it was fetched with
//     (AppIDs).
//   - Signed ID tokens, from Mint with the current key or from SignRS256
//     with any key.
//
// # References
//
//   - Research 21: docs/research/21-oidc-token-exchange.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": idtokentest stays below idtoken).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 300 and 988, which put it under adapters/oidc/oidctest; core
//     tests would then import an adapter, so decision 0044 keeps it here.
//   - https://github.com/weaveplatform/artifactweave/issues/1073
//
// # Status
//
// Implemented P4 with the OIDC token exchange (pull request 145). Moved here
// in P9d.
package idtokentest
