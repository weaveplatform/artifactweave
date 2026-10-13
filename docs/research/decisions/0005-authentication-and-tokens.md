# 0005: Authentication, tokens and compatibility version strings

Status: Proposed

## Context

Every Artifactory client authenticates one of several ways: HTTP Basic with a password,
an API key, an access token or a reference token as the password; a `Bearer` header with
a JWT or a reference token; the `X-JFrog-Art-Api` header; or anonymously where the
instance allows it. Docker clients use the registry token flow. The JFrog CLI stores and
inspects access tokens, and the Terraform providers create scoped tokens.

Clients also read the server's version. The Terraform providers call
`GET /artifactory/api/system/version` at configure time and parse `version` (the platform
provider also reads `GET /access/api/v1/system/version` and parses `name`); per-resource
gates go up to Artifactory 7.146 and Access 7.155. The JFrog CLI gates multipart upload on
7.82.2. None of the providers checks the licence endpoint. Every `jf` command posts usage
telemetry and, on recent versions, a jfconnect metrics log.

## Decision

**Mechanisms accepted on every endpoint:** Basic (password, access token or reference
token as the password; the username must match the token subject), `Bearer` (JWT or
reference token), `X-JFrog-Art-Api` (reference tokens only), and anonymous when
`anonAccessEnabled` is on and the anonymous principal has permission. Basic auth can be
disabled platform-wide through the Access configuration endpoint.

**Access tokens** are JWTs signed with a per-instance RS256 key. Subject
`jfac@<service-id>/users/<name>`, the scope string as issued, `token_id` persisted for
listing and revocation, optional refresh token, optional reference token. Scope strings
follow the grammar in [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
and only narrow the principal's permissions. Defaults: `expires_in` 3600 (configurable
through `default_expiry`), `0` means never and is admin-only, tokens shorter than the
21600 second revocable threshold are non-revocable unless `force_revocable`.

**Reference tokens** are 64 characters matching `cmVmd[A-Za-z0-9]{59}`, stored by hash,
resolvable to their JWT.

**JWT payload encoding quirk.** The JFrog CLI extracts the username by decoding the JWT
payload with Go's `base64.RawStdEncoding` (standard alphabet, no padding) and reading
`sub`. Payloads are therefore padded with whitespace until their encoding contains no
`-`, `_` or `=`, and the subject always contains `/users/<name>`.

*As implemented (#74):* rather than padding until the encoding happens to be clean, the
payload escapes `>`, `?`, `~`, DEL and every non-ASCII rune inside JSON strings as `\u`
sequences. In base64 a character is `-` or `_` only when the low six bits of the third
byte of a group are `111110` or `111111`, and among ASCII bytes only those four have such
bits; JSON emits them only inside strings. The encoding is therefore clean by
construction, with no search.

*Keys and lifecycle (#74):*
- **Signing key:** the RS256 key is a sealed `KeyStore` signing key under the `access-token-`
  prefix, created on first start.
- **Rotation:** the replaced keys keep verifying until the latest expiry of the tokens stored
  at rotation, or for ever when one never expires. The retirement record is the
  `token.signing_keys` setting.
- **Issuer and subject:** the issuer is `jfac@<uuid>`, taken from the instance id `jfrt@<uuid>`
  created at first start.
- **Records:** every token's record is stored, as only digests of its secrets.
  Verification needs the record, so revocation is deletion. GC removes records
  `DefaultRefreshGrace` after expiry, the window in which their refresh token still works.

**Token API.** `POST /access/api/v1/tokens` and the legacy form `POST /api/security/token`
issue tokens from the same service, under these rules:
- A non-administrator may mint only identity tokens (`applied-permissions/user`) for
  themselves; an administrator may name any user and any scope.
- A token whose scope is narrower than its user's identity (groups or resources only) may not
  mint tokens at all, so a narrowed token cannot obtain a broader one.
- The refresh grant is authenticated by the refresh token itself, so both create routes let a
  request through without other credentials, and the handlers require a user for every other
  grant. A refresh request that sends an expired token in `Authorization` is refused (401) by
  the middleware before it reaches the handler.
- Every token has a record. Non-revocable tokens (shorter than the revocable threshold and not
  `force_revocable`) are left out of the token lists and refuse revocation, as in Artifactory,
  but still read back by id: the `artifactory_scoped_token` resource reads its token after
  creating it.
- `GET /api/security/encryptedPassword` answers, in place of an encrypted password, a
  non-expiring, revocable reference token bound to the caller. Only a password login may ask
  for one.

**Docker token endpoint.** `GET /v2/` without credentials answers `401` with a
`WWW-Authenticate: Bearer realm=...,service=...` challenge; the realm issues short-lived
JWTs scoped `repository:<image>:pull,push`. The exact `service` value and `expires_in`
are captured from a live Artifactory before they are fixed.

**Passwords** are hashed with Argon2id. Lock policy and expiration policy follow the
legacy security API shapes (real JSON booleans and integers, as the Terraform provider
decodes them; string spellings such as `"true"` and `"60"` are accepted on write).

**Account policies** are enforced by the authenticator:
- **The user lock policy** (`enabled`, `loginAttempts`, default 5) counts consecutive
  wrong passwords only while it is enabled. Each failure is counted and, at the limit,
  the account locked, in one database statement, so concurrent attempts cannot all
  stay below it. A correct password resets the count.
- **A locked account** answers exactly as a wrong password does, after the same
  hashing work, so neither the message nor the timing tells that the account exists.
  Unlocking (one, several or all users) and disabling the policy clear locks.
- **A password expires** when an administrator expires it (whatever the policy) or
  when the expiration policy is enabled and the password is older than
  `passwordMaxAge` days. Age counts from the recorded change, so passwords set before
  it was recorded never expire on their own. Expiry is reported only after the
  password verifies.
- **The policies are cached** for 10 seconds per instance. A change made through an
  instance applies there at once, and elsewhere within the 10 seconds.

**API keys** are shims: creation answers `400` with a message that API keys are retired,
lookup answers `404`, and `X-JFrog-Art-Api` accepts reference tokens.

**Seeded principals.** First start creates `admin` (password from configuration),
`anonymous`, the `readers` group and the `Anything` and `Any Remote` permission targets,
and the `artifactory-build-info` repository.

**Compatibility version strings.** `/artifactory/api/system/version` returns a
configurable Artifactory-compatible `version`, default 7.155.0 or newer, with the real
artifactweave version in `revision` and `addons`. `/access/api/v1/system/version` returns
`{"name":"<same>","version":"<same>"}`. `/api/system/license` is honest
(`type: "artifactweave"`); no client gates on it. `POST /api/system/usage` and
`POST /jfconnect/api/v1/backoffice/metrics/log` answer 2xx and discard the body.

## Rationale

The mechanisms are dictated by the clients. A JWT with a JFrog-shaped subject is what
`jf c add` parses and what the providers pass back; a reference token is what the API-key
replacement guidance tells users to use. Retiring API keys matches JFrog's own end of
life and removes a weak credential.

Reporting a recent Artifactory version is necessary: the providers silently disable
newer code paths when the version is older, and the CLI expects the multipart probe to
exist. Reporting the real artifactweave version elsewhere keeps the lie bounded.

**Alternatives considered:**
- *Opaque tokens only*: simpler, but the CLI and providers read the JWT payload.
- *Symmetric JWT signing*: fine for one process, but RS256 lets a future UI or edge
  verify without the secret.
- *Reporting the true artifactweave version*: breaks every client gate.

## Constraints

- The signing key is sealed at rest ([0017](0017-sealed-columns-and-master-key.md)) and
  rotation keeps the previous key for verification until the longest token expiry.
- Token scopes are evaluated after the permission model, never instead of it.
- Anonymous access is off by default.

## Verification

- Token issue, refresh, revoke and reference-token tests through the Access API.
- A test that every issued JWT payload decodes with `base64.RawStdEncoding`.
- `jf c add` and `jf rt access-token-create` in the acceptance suite.
- `artifactory_scoped_token` and `platform_permission` Terraform acceptance.
- Docker token flow in the OCI conformance job.
- A test that the version endpoints report the configured compatibility version and that
  the usage and metrics stubs answer 2xx.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md),
  tokens, auth mechanisms and JSON shapes
- [08-terraform-providers](../08-terraform-providers.md), configure-time probes
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), token and version rows
- <https://docs.jfrog.com/administration/docs/access-tokens>
- <https://docs.jfrog.com/user-management/docs/identity-tokens>
