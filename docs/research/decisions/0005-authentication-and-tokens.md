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

**Docker token endpoint.** `GET /v2/` without credentials answers `401` with a
`WWW-Authenticate: Bearer realm=...,service=...` challenge; the realm issues short-lived
JWTs scoped `repository:<image>:pull,push`. The exact `service` value and `expires_in`
are captured from a live Artifactory before they are fixed.

**Passwords** are hashed with Argon2id. Lock policy and expiration policy follow the
legacy security API shapes (real JSON booleans and integers, as the Terraform provider
decodes them).

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
