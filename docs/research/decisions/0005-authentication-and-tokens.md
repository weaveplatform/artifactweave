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
- **Records:** every token's record is stored, as only digests of its secrets. The one
  exception is the encrypted password's reference token, which is kept sealed so it can be
  shown again (#106). Verification needs the record, so revocation is deletion. GC removes
  records `DefaultRefreshGrace` after expiry, the window in which their refresh token still
  works.

**Token API.** `POST /access/api/v1/tokens` and the legacy form `POST /api/security/token`
issue tokens from the same service, under these rules:
- A non-administrator may mint only identity tokens (`applied-permissions/user`) for
  themselves; an administrator may name any user and any scope.
- A token whose scope is narrower than its user's identity (groups or resources only) may not
  mint tokens at all, so a narrowed token cannot obtain a broader one. For the same reason it
  may not set its user's password (`PUT /access/api/v2/users/{username}/password`), which would
  turn it into a credential with all of the user's rights (#84).
- The refresh grant is authenticated by the refresh token itself, so both create routes let a
  request through without other credentials, and the handlers require a user for every other
  grant.
- *Refresh statuses (#106):* both create routes answer a refresh grant as follows:
  - **400** for an unknown, expired, revoked or malformed refresh token. This is inferred, as
    the analogue of OAuth's `invalid_grant`; Access documents no status for it.
  - **403** when the refresh token belongs to another user than the caller. The documented
    rule is "The authenticated user must match the user of the access token being refreshed
    (identified by the refresh token)", and the operation lists 403 as forbidden
    (`.specoracle/administration-jfrog-platform-rest-apis/createtoken.json`). There is no
    administrator exception.
  - **401** for missing or invalid credentials, including a revoked or forged token presented
    as the token being refreshed.
  - A refresh with no credentials at all still succeeds on the refresh token alone.
- *Refresh with an expired token (#106):* the JFrog CLI refreshes with the access token it is
  replacing as its `Authorization: Bearer` credential (jfrog-cli-core
  `utils/config/tokenrefresh.go`; jfrog-client-go `access/services/accesstoken.go`,
  `handleUnauthenticated`). It refreshes because that token is expiring or has already
  expired. The exception applies only to `POST /access/api/v1/tokens`, and only to a Bearer
  JWT whose signature, key, record and subject verify and whose user is enabled, but whose
  `exp` has passed. Such a request reaches the handler with no principal and with the token.
  The handler then accepts only a refresh grant whose refresh token belongs to that token:
  - another user's refresh token answers 403;
  - another token of the same user answers 400, as an unknown one does, so the answer never
    confirms a pairing;
  - any other grant answers 401.

  Revoked tokens, forged tokens, reference tokens, and an expired token on any other route
  still answer 401. The record outlives `exp` by `DefaultRefreshGrace`, which bounds the
  window.
- *Legacy refresh (#106):* the JFrog CLI refreshes a legacy Artifactory refresh token (the one
  `jf c import` stores) through `POST /api/security/token`. It sends no `Authorization` header,
  and the form carries the current, possibly expired, token as `access_token` (jfrog-client-go
  `artifactory/services/security.go`, `RefreshToken` and `buildRefreshTokenUrlValues`;
  jfrog-cli-core `utils/config/tokenrefresh.go`, `refreshArtifactoryExpiredToken` with
  `noCredsDetails`). The handler applies the following rules:
  - `access_token` is required (400 without it);
  - its signature, key, record, subject and user are verified with expiry ignored (401 if
    any fails);
  - it must pair with `refresh_token` (same token id), with the statuses above;
  - an authenticated caller must also be its user (403).
- Every token has a record. Non-revocable tokens (shorter than the revocable threshold and not
  `force_revocable`) are left out of the token lists and refuse revocation, as in Artifactory,
  but still read back by id: the `artifactory_scoped_token` resource reads its token after
  creating it.
- `GET /api/security/encryptedPassword` answers, in place of an encrypted password, a
  non-expiring, revocable reference token bound to the caller. Only a password login may ask
  for one.
- *The encrypted password follows the password (#106):* as with Artifactory's encrypted
  password, a user has one value at a time.
  - **One per user:** the user holds at most one token of purpose `encrypted_password`
    (enforced by a unique `(subject, purpose)` index).
  - **Same value on repeat:** its reference token is stored sealed (`tokens.secret`, resealed
    with the other sealed columns), so a repeat request answers the same value.
  - **Revocation:** the identity service revokes it whenever the password is set or an
    administrator expires it, for one user or for all at once. Every set goes through its
    `setPassword`, whichever API makes it. Sending back the current password, as Terraform
    does on every update, is no change and keeps the encrypted password. The next request
    then issues a new value.
  - **Refusal at authentication:** the age policy expires a password without any write to
    revoke on. Authentication therefore refuses an encrypted-password token whenever the
    password itself would be refused: expired (`ErrPasswordExpired`), internal password
    disabled, or no password set.

**OIDC token exchange** ([research 21](../21-oidc-token-exchange.md)).
`POST /access/api/v1/oidc/token` needs no credentials: the subject token is the
credential. Providers and identity mappings are configuration documents, admin only,
stored in their wire form so the jfrog/platform provider reads back what it wrote.
- **Verification.** The ID token is verified against the provider's keys, found
  through `<issuer_url>/.well-known/openid-configuration` and its JWKS and cached for
  15 minutes; a key the cache lacks forces one early fetch, at most every 30 seconds.
  Asymmetric algorithms only (RS, PS, ES, EdDSA). `iss` must be `token_issuer` when a
  generic or Azure provider sets it, otherwise `issuer_url`; `aud` must hold the
  provider's audience when it has one; the token must carry `exp`, with a minute of
  leeway on every time. A GitHub provider that is not permissive also requires
  `repository_owner` to equal its organisation. Azure's `azure_app_id` asks the JWKS
  for the application's keys. Keys are fetched directly, or through the environment's
  proxy when `use_default_proxy` is set.
- **Mapping.** A named `identity_mapping_name` is the only candidate. Otherwise the
  request's project's mappings come first, then global ones, then lower priorities, a
  mapping without one last, then creation order; another project's mappings never
  apply. A claim matches with `*` (not across `/`) and `**` wildcards, a list matches
  when any element does, an object matches its nested claims, and a dotted name reaches
  into nested objects. `username_pattern` and `groups_pattern` take `{{claim}}`,
  optionally followed by `| replacement` or `| regex | replacement`.
- **The token.** Its lifetime is `token_spec.expires_in`, one hour when unset, at most
  24 hours. A named or patterned user may not exist: the token is marked
  `transient`, and authenticates as the stored user of that name when there is one,
  otherwise as a transient user in the auto-join groups. A mapping without a user
  issues for the synthetic subject `oidc-<provider>-<mapping>`, marked `synthetic`,
  which is never a stored user and acts through its scope's groups alone; such a
  mapping must therefore name groups only. Every other token still needs a stored,
  enabled user. The description records the provider, the mapping and the request's
  CI fields; `audience`, `application_key` and the `gh_*` fields are accepted and
  ignored.
- **Statuses.** 400 for a malformed request, an unknown provider or mapping, or no
  matching mapping; 401 for a token that does not verify, including when the
  provider's keys cannot be fetched, which is logged at WARN. Management answers 404
  for an unknown provider or mapping, 400 for an invalid body or a name in use.

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

*Access v2 and the identity configuration (#81):*
- **One record, two APIs.** `/access/api/v2/users` and `/groups` manage the same records
  as the legacy security API. Creation is create-only: a taken name answers `409`, not a
  replacement. Deleting a built-in user answers `403` in v2, but `400` in the legacy API
  as before.
- **The basic authentication switch** refuses a password in `Basic` with `401` when it is
  off. An access or reference token sent as the Basic password still works, as does
  `Bearer`. When the switch cannot be read, the password is checked as usual; the policy
  read then fails the request, so a broken settings store never lets a password in that
  the switch would refuse. The switch is cached with the account policies.
- **An expired password may set its own replacement.** The middleware lets a correct but
  expired password through for exactly one request:
  `PUT /access/api/v2/users/{own name}/password`. Without that request, a user whose
  password expired could not choose a new one.
- **The password policy** (`uppercase`, `lowercase`, `digit`, `special_char`, `length`,
  `not_match_old`) is enforced by the identity service whenever either API sets a password.
  Passwords already stored are not checked.
  - *Defaults (documented: docs.jfrog.com, supported Access configurations,
    `security.password-policy`).* While no policy is stored, the policy requires no
    character classes, a length of at least 4, and a new password that differs from the old
    one. A password may never match the username, compared without case. The API has no
    field for that last rule, so it always applies.
  - *PUT and PATCH (documented: `createPasswordPolicy`, `updatePasswordPolicy`).* `PUT`
    replaces the policy, and a field it leaves out returns to its default. `PATCH` merges
    into the stored policy.
  - *A resent password is no change (observed: terraform-provider-artifactory).* A user
    record update (legacy `PUT`/`POST`, v2 `PATCH`) that sends the current password back
    leaves it as it is: no policy check, no `not_match_old`, and no reset of the password's
    age or expiry. Only `PUT …/{username}/password` treats the same password as a reuse.
    - *Evidence:* `artifactory_user`'s Update always sends `Password: plan.Password`
      (`pkg/artifactory/resource/user/user.go`, lines 810–820), as PATCH
      `access/api/v2/users/{name}`, or as POST `api/security/users/{name}` on older
      versions (lines 468–486). `TestAccUser_full_groups`
      (`resource_artifactory_user_test.go`, lines 138–209) updates a user with the same
      password against a real Artifactory, where `not-match-old` defaults to true.
    - *Inferred:* no expiry reset, because resetting it would extend the expiry on every
      `terraform apply`.
- **Deleting groups** *(inferred: no JFrog source protects `readers`, and
  `jf rt group-delete` makes no exception).* Any group may be deleted through either API,
  the seeded `readers` included. Its grants are removed from every permission (such as the
  seeded `Anything`) in the same transaction, so a later group of the same name inherits
  nothing. Seeding runs once, so `readers` does not come back.
- **Group create and update** *(documented: `createGroup`, `updateGroup`; observed:
  `platform_group` PATCHes its whole model).*
  - A create naming an unknown member answers `404` "One or more users in members were not
    found".
  - The update request carries neither name, realm, realm attributes nor members, and
    Access does not change a realm. A PATCH may still send `name`, `realm` and
    `realm_attributes` with their stored values, but answers `400` when one differs.
    `members` is ignored, since it has its own endpoint.
- **Readers of users and groups.** Administrators, and tokens whose scope covers
  `system:identities:r`, may list and read users and groups, as the permission evaluator
  decides ([0006](0006-authorization-native-permission-model.md)). A user may also read their own record. All
  writes are admin-only, except a user changing their own password.

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
- OIDC: `internal/service/oidc` and `internal/auth/idtoken` tests against a fake issuer
  (`idtokentest`); `oidc.feature`, where the real `jf eot` exchanges a token and
  downloads with it; `deploy/artifactweave/terraform/oidc` in the `terraform` job.
- Docker token flow in the OCI conformance job.
- A test that the version endpoints report the configured compatibility version and that
  the usage and metrics stubs answer 2xx.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md),
  tokens, auth mechanisms and JSON shapes
- [08-terraform-providers](../08-terraform-providers.md), configure-time probes
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), token and version rows
- [21-oidc-token-exchange](../21-oidc-token-exchange.md), the exchange's wire behaviour
- <https://docs.jfrog.com/administration/docs/access-tokens>
- <https://docs.jfrog.com/user-management/docs/identity-tokens>
