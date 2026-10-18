# 0005: Authentication, tokens and compatibility version strings

Status: Proposed

Checked against: Artifactory 7.161.32 (self-managed; SaaS 7.174.0); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#artifactory-7x-check) records it.

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
disabled platform-wide through the Access configuration endpoint. A password does not create
tokens through the Access token API unless `token.allow_basic_auth` is on (**Token API**
below).

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
- *Token creation with a password (#385):* a user name and password sent with Basic create
  tokens through `POST /access/api/v1/tokens` only while the application setting
  `token.allow_basic_auth` is on. It is off by default, as 7.x ships `token.allow-basic-auth`
  ("Enable Token Generation via API";
  [research 05](../05-platform-identity-and-security-apis.md#authentication-accepted-on-every-endpoint),
  item 7). It is configuration only, because 7.x has no REST API for it either.
  - A refused request answers 401 with `Unsupported authentication method Basic`, the code and
    message Access gives Basic credentials it does not take **[I]**.
  - An access or reference token, as Bearer or as the Basic password, is not a password and is
    not refused; nor is a refresh grant, whose credential is its refresh token.
  - The legacy `POST /api/security/token` takes a password whatever the setting says **[I]**.
    In 7.x it is Artifactory's own route, where `jf c add` with a password swaps it for a token
    and `jf rt atc` creates one, so refusing it would break every such configuration.
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

**Docker token endpoint** (values from [research 17](../17-undocumented-shapes.md),
behaviour from [research 23](../23-docker-oci-registry.md) section 3; routes in
[0003](0003-repository-model.md), registry behaviour in [0031](0031-docker-oci-registry.md)):
- **Challenge.** Every 401 from the registry carries
  `WWW-Authenticate: Bearer realm="<base>/artifactory/api/docker/<repo|null>/v2/token",service="<host[:port]>"`,
  plus `,scope="repository:<name>:<actions>"` on a repository operation. The host is the
  one the client addressed, from the base-URL resolver, with any non-default port. The
  bare `/v2/` probe uses the repository `null`, and under the subdomain option the realm
  names the subdomain's repository.
- **Issuing.** `GET` on the realm (per repository, `null`, or `/v2/token`) with Basic
  credentials (a password, an access token or a reference token) or a Bearer token, or
  anonymously when anonymous access is on. `POST` answers 405. `account`, `client_id` and
  `offline_token` are ignored. The body is
  `{"token","access_token","expires_in","issued_at"}`, with `token` and `access_token`
  equal.
- **The token** is a short-lived access token from the token service: the same RS256 key,
  issuer and subject form as every access token, with the registry grants in its
  `registry` claim, `repository:<name>:<actions>` for each requested scope with only the
  actions the caller's permissions allow (`pull` needs Read, `push` Deploy, `delete`
  Delete, `*` all three). Unlike other tokens it is never stored, as Artifactory stores no
  token below its persistence threshold. Its lifetime is 3600 seconds, or less when the
  credentials that obtained it expire sooner. It is accepted only on the registry's routes
  and answers 401 anywhere else.
- **It stands for the credential that obtained it** (#248). Its `scp` is that credential's
  scope (`applied-permissions/user` for a password), so it never acts more broadly. A
  token from an OIDC exchange passes on its `transient` or `synthetic` marker in the
  `idn` claim, so the registry token resolves to the same user the exchanged token does,
  by the rules under **The token** above: the stored user of that name when there is one,
  otherwise a transient user in the auto-join groups, and a synthetic subject through its
  scope's groups alone. A registry token obtained with a token carries that token's id
  in the `ptk` claim. Nothing stores a registry token, so the parent must still be stored
  on every use, and revoking the parent ends every registry token descended from it; a
  registry token that obtains another passes its own parent on. One obtained with a
  password has no parent.
- **Why it is issued, not refused, for an OIDC-exchanged user** (research 21, section
  3a). JFrog's own tooling uses an exchanged token for the registry: `setup-jfrog-cli`
  with `oidc-provider-name` configures the JFrog CLI with the exchanged token and its
  username and exports both as `oidc-user` and `oidc-token`; its documented examples then
  run `jf docker push`, which logs the Docker client in with that username and the
  exchanged token as the password. Access documents that such a token's user may be
  transient, with its permissions from the groups scope. The exchanged token must
  therefore work for `docker login` and the token flow behind it. The secure form chosen:
  grants are the requested actions intersected with the exchanged token's scope and the
  current permissions, at issue and on every use; the lifetime never outlasts the
  exchanged token; and revoking the exchanged token revokes the registry token.
- **On each registry request** the granted actions for the addressed `<name>` are
  intersected with the caller's current permissions, under the token's scope, so a token
  for one image never authorizes another and a removed permission applies at once. A
  disabled or deleted stored user's token answers 401, as does one whose parent token has
  been revoked.
- **Basic on the registry.** Clients that skip the token flow may send Basic credentials on
  every request; they are checked as on any other route.
- **`auth` endpoints.** `GET /artifactory/api/docker/<repo>/v2/auth` and `.../v1/auth`
  answer the `config.json` entry `{"auth": base64(<user>:<secret>), "email"}`, where the
  secret is the credential the request authenticated with; an anonymous request answers
  401. Which secret Artifactory returns for token callers is on the live-capture list.
- **Artifactory 7.x.** The audit against KB, Jira and client sources found three
  differences ([research 17](../17-undocumented-shapes.md#docker-token-challenge-and-token-endpoint),
  [research 23 section 12](../23-docker-oci-registry.md#12-artifactory-7x-check)):
  - 7.x differs: on the repository-path method 7.71.5 challenges with
    `realm="<host>/v2/token"` and a scope that names the image without the repository key
    ([RTFACT-30264](https://jfrog.atlassian.net/browse/RTFACT-30264)); a deviation
    registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).
  - 7.x differs: the token body is `{"token","expires_in"}`, with neither `access_token` nor
    `issued_at` ([RTFACT-25069](https://jfrog.atlassian.net/browse/RTFACT-25069),
    [RTFACT-26227](https://jfrog.atlassian.net/browse/RTFACT-26227),
    [the docker.art.local walk-through](https://jfrog.com/help/r/how-to-troubleshoot-docker-problems/docker-login-docker.art.local));
    a deviation registered by [#378](https://github.com/weaveplatform/artifactweave/issues/378).
  - 7.x differs: the `auth` endpoints answer the entry keyed by the registry URL,
    `{"https://<host>": {"auth", "email"}}`
    ([additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information#set-your-docker-credentials-manually));
    fixed by [#414](https://github.com/weaveplatform/artifactweave/issues/414).

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
and the `artifactory-build-info` repository. Neither permission grants `anonymous`, as
Artifactory ships them since 7.84.3: `Anything` grants `readers` read on every repository,
and `Any Remote` grants no one
([0006](0006-authorization-native-permission-model.md#seeded-permissions-383)).

**Compatibility version strings.** `/artifactory/api/system/version` returns a
configurable Artifactory-compatible `version`, default 7.155.0 or newer, with the real
artifactweave version in `revision` and `addons`. `/access/api/v1/system/version` returns
`{"name":"<same>","version":"<same>"}`. The Artifactory version answers any signed-in
caller, and anonymous only while anonymous access is on, because 7.x refuses it to callers
without credentials ([research 01](../01-jfrog-api-landscape.md#authentication-schemes));
the Access version stays open. `/api/system/license` is honest
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
- *A separate registry JWT with its own key, or a stored access token for the registry*:
  a second key to rotate, or a stored record for every `docker pull`, which Artifactory
  avoids with its persistence threshold. A scoped, unstored access token reuses the
  signing and verification already in place.

## Artifactory 7.x check

Audited against 7.161.32 (SaaS 7.174.0) on 2026-10-18 with the KB, Jira and client sources
([#378](https://github.com/weaveplatform/artifactweave/issues/378);
[research 05](../05-platform-identity-and-security-apis.md#artifactory-7x-check),
[research 21](../21-oidc-token-exchange.md#6-artifactory-7x-check)). The token routes, fields
and scope grammar, the legacy token shapes, the Access v2 users and groups, the account policy
endpoints and the OIDC configuration and exchange shapes match 7.x. So does who may use the
legacy `GET /api/security/token` and `POST /api/security/token/revoke`: an administrator
alone, "Requires an admin user"
([deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis),
Get Tokens and Revoke Token). A user is refused even for their own token, which they list and
revoke through `/access/api/v1/tokens` instead
([#382](https://github.com/weaveplatform/artifactweave/issues/382)). These differ.

**The default token lifetime.** JFrog's sources disagree:
- the [access-tokens page](https://docs.jfrog.com/administration/docs/access-tokens)
  (updated 2026-07-06) says "The default expiry is 3600 seconds";
- the Access configuration reference gives `token.default-expiry: 31536000`
  ([supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)),
  and the [recommended configurations](https://docs.jfrog.com/administration/reference/recommended-configurations)
  page says "Default value: 1 year";
- the JFrog CLI's `jf atc --expiry` help says the platform default is "1 year by default"
  (jfrog-cli `utils/cliutils/commandsflags.go`, `atcExpiry`).

Maintainer decision (2026-10-18): the default becomes one year, following the configuration
reference and the CLI.
- 7.x differs: a token created without `expires_in` lives one year, and `default_expiry`
  reads 31536000 until an administrator changes it (the sources above); fixed by
  [#406](https://github.com/weaveplatform/artifactweave/issues/406).

**Tokens:**
- 7.x differs: a non-administrator's `expires_in: 0` answers 200 or 403, never 400. With
  `token.max-expiry` at its default 0, "users can set the `expires_in` parameter to any value"
  ([recommended configurations](https://docs.jfrog.com/administration/reference/recommended-configurations)),
  and a refused request is 403, "The requested token details are forbidden"
  ([createtoken](https://docs.jfrog.com/administration/reference/createtoken)); fixed by
  [#406](https://github.com/weaveplatform/artifactweave/issues/406).
- 7.x differs: a refreshable token is revocable, and listed, whatever its lifetime
  ([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue):
  "created with `force_revocable: true` or `refreshable: true`"); fixed by
  [#406](https://github.com/weaveplatform/artifactweave/issues/406).
- 7.x differs: revoking a non-revocable token by id answers 400 (KB
  [000005622](https://jfrog.com/help/r/artifactory-how-to-troubleshoot-failed-to-revoke-tokens-when-trying-to-revoke-a-token-through-the-ui));
  fixed by [#406](https://github.com/weaveplatform/artifactweave/issues/406).
- 7.x differs: revoking a non-revocable token by value answers 404, "Token not found or not
  revocable"
  ([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue));
  fixed by [#406](https://github.com/weaveplatform/artifactweave/issues/406).
- 7.x differs: a token whose lifetime is below `persistent-expiry-threshold` (10800 s) is not
  stored, so its id reads 404
  ([supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)).
  artifactweave stores every token so that `artifactory_scoped_token` can read its token back,
  but that resource treats a 404 after create as a warning only
  (`resource_artifactory_scoped_token.go`, lines 512 to 523, terraform-provider-artifactory
  v12.11.14). Kept, as a deviation registered by #378.
- A password sent with Basic creates tokens through the Access token API only once an
  administrator turns on "Enable Token Generation via API" (`token.allow-basic-auth: false` by
  default; [createtoken](https://docs.jfrog.com/administration/reference/createtoken),
  [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations),
  [security configuration](https://docs.jfrog.com/administration/docs/security-configuration)).
  artifactweave matches it with `token.allow_basic_auth`, off by default (**Token API** above,
  [#385](https://github.com/weaveplatform/artifactweave/issues/385)). The 401 it answers and
  the legacy route staying open are inferred, and on
  [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- 7.x differs: a token for a user that does not exist authenticates as a transient user, and a
  groups-scoped one acts with its groups' permissions
  ([access tokens](https://docs.jfrog.com/administration/docs/access-tokens), "Support
  Authentication for Non-existing Users"). Here an administrator may mint such a token, but
  it answers 401 on every use; only OIDC tokens act as transient users (**The token**
  above); fixed by [#406](https://github.com/weaveplatform/artifactweave/issues/406).

**Account policies and seeded principals:**
- 7.x differs: failed Basic logins also suspend the account for a moment, answering 403 for a
  delay that grows to at most 60 seconds, and `seconds-to-unlock` defaults to 1
  ([security configuration](https://docs.jfrog.com/administration/docs/security-configuration),
  "Temporary Login Suspension";
  [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations));
  fixed by [#408](https://github.com/weaveplatform/artifactweave/issues/408).
- Since 7.84.3 `anonymous` is in neither the `Anything` nor the `Any Remote` permission
  ([permissions](https://docs.jfrog.com/administration/docs/permissions)), and artifactweave
  seeds both that way. Decision
  [0006](0006-authorization-native-permission-model.md#seeded-permissions-383) records what
  the seed grants.

**OIDC token exchange** ([research 21](../21-oidc-token-exchange.md#3-matching-and-the-token-issued)
section 3 describes each point, from
[OpenID Connect integration](https://docs.jfrog.com/administration/docs/openid-connect-integration)):
- 7.x differs: a mapping that resolves a username adds `applied-permissions/user`, so the
  token acts through its named groups and the auto-join groups; from 7.133.10 not with a
  project roles scope (JA-18771 in
  [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed),
  JA-19609 in
  [7.133.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-713310-self-managed));
  fixed by [#409](https://github.com/weaveplatform/artifactweave/issues/409).
- 7.x differs: one mapping may combine a user with a groups pattern, and a user pattern with a
  groups pattern; it may not combine groups with a groups pattern, a user with a user pattern,
  or admin with anything (the page's combination table); fixed by
  [#409](https://github.com/weaveplatform/artifactweave/issues/409).
- 7.x differs: a two-part pattern matches its template against the claim, the claim's place
  being a capture group, and a claim that does not match fails the request ("Dynamic
  Mapping"); fixed by [#409](https://github.com/weaveplatform/artifactweave/issues/409).
- 7.x differs: from 7.130.0 a pattern may chain more than three parts and use
  `op:replaceAll`, `op:replaceFirst` and `op:replaceLast` ("Advanced Dynamic Mappings");
  fixed by [#409](https://github.com/weaveplatform/artifactweave/issues/409).
- 7.x differs: a Resource Scope mapping turns a claim such as `jfrog_scope` into an
  `artifact:` scope (SaaS
  [7.172.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71721-saas));
  fixed by [#409](https://github.com/weaveplatform/artifactweave/issues/409).
- 7.x differs: with no `project_key`, "all identity mappings compete equally based on priority
  and creation time", a project's mappings included ("Identity Mappings"); fixed by
  [#409](https://github.com/weaveplatform/artifactweave/issues/409).

## Constraints

- The signing key is sealed at rest ([0017](0017-sealed-columns-and-master-key.md)) and
  rotation keeps the previous key for verification until the longest token expiry.
- Token scopes are evaluated after the permission model, never instead of it.
- Anonymous access is off by default.

## Verification

- Token issue, refresh, revoke and reference-token tests through the Access API.
- A test that every issued JWT payload decodes with `base64.RawStdEncoding`.
- `jf c add` and `jf rt access-token-create` in the acceptance suite.
- Token creation with a password: `TestAccessTokenWithPassword`, the status table and the
  route by principal matrix at the default, and `basic_auth_tokens.feature`, where the Access
  route refuses the password, the legacy route and `jf c add` with a password still work, and
  the token from the legacy route creates one through the Access route.
- `artifactory_scoped_token` and `platform_permission` Terraform acceptance.
- OIDC: `internal/service/oidc` and `internal/auth/idtoken` tests against a fake issuer
  (`idtokentest`); `oidc.feature`, where the real `jf eot` exchanges a token and
  downloads with it; `deploy/artifactweave/terraform/oidc` in the `terraform` job.
- Docker token flow in the OCI conformance job, and `docker login` and `crane auth login`
  against the binary; tests for scope intersection, refusal across images and routes, and
  expiry capped by the credentials.
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
