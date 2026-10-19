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
and only narrow the principal's permissions, apart from `applied-permissions/admin`, which
makes an administrator's token whatever its subject
([0006](0006-authorization-native-permission-model.md#artifactory-7x-check)). Defaults:
- `expires_in` is 31536000 seconds, one year, configurable through `default_expiry`. JFrog's
  access-tokens page says 3600 instead; [the 7.x check](#artifactory-7x-check) records the
  conflict, which is on #216.
- The legacy `POST /api/security/token` keeps its own default of 3600 seconds.
- `0` means never. Only an administrator may ask for it; anyone else is answered 403.
- Tokens shorter than the 21600 second revocable threshold are non-revocable unless
  `force_revocable` or `refreshable`.

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
  created at first start. `GET /artifactory/api/system/service_id` returns that id to
  administrators, and the legacy scope `jfrt@<id>:admin` that `jf rt atc --grant-admin` builds
  from it is `applied-permissions/admin` (#391).
- **Records:** every token's record is stored, as only digests of its secrets. The one
  exception is the encrypted password's reference token, which is kept sealed so it can be
  shown again (#106). Verification needs the record, so revocation is deletion. GC removes
  records `DefaultRefreshGrace` after expiry, the window in which their refresh token still
  works.

**Token API.** `POST /access/api/v1/tokens` and the legacy form `POST /api/security/token`
issue tokens from the same service, under these rules:
- A non-administrator may mint only identity tokens (`applied-permissions/user`) for
  themselves; an administrator may name any user and any scope.
- *Transient users (#406):* an administrator may name a user that is not stored, and the token
  is then marked `transient`. It authenticates as the stored user of that name when there is
  one. Otherwise it authenticates as a transient user, held for the request and never saved,
  as JFrog's [access-tokens page](https://docs.jfrog.com/administration/docs/access-tokens)
  describes ("Support Authentication for Non-existing Users", "Transient Users and Auto Join
  Groups"). Its permissions are what the scope gives:
  - `applied-permissions/user` gives grants to the name and to the auto-join groups;
  - a groups scope gives the groups it names alone;
  - `applied-permissions/admin` makes an administrator.

  A transient user has no record, so `GET` and `DELETE /api/security/users/<name>` (and their
  Access v2 forms) answer 404, and deleting the name revokes nothing. Its tokens are listed,
  read and revoked through the token API like any other. Creating a user of that name makes
  the tokens act as that user, and deleting that user then revokes them with the user's own.
  Only an administrator's token is marked: a non-administrator mints for themselves alone.
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
- Every token has a record. Non-revocable tokens (shorter than the revocable threshold and
  neither `force_revocable` nor `refreshable`) are left out of the token lists and refuse
  revocation, as in Artifactory: 400 by id, and 404 by value, "Token not found or not
  revocable". They still read back by id: the `artifactory_scoped_token` resource reads its
  token after creating it.
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
- **Mapping.** A named `identity_mapping_name` is the only candidate. With a
  `project_key`, that project's mappings come first, then global ones, and another
  project's mappings are not tried. Without one, every mapping competes: "When no
  `project_key` is supplied, all identity mappings compete equally based on priority and
  creation time" ("Identity Mappings"). Within each tier lower priorities come first, a
  mapping without one last, then creation order. The token belongs to the project of
  the mapping that matched, if it has one. A claim matches with `*` (not across `/`) and
  `**` wildcards, a list matches when any element does, an object matches its nested
  claims, and a dotted name reaches into nested objects.
- **Combinations** (the page's table under "Identity Mappings"). An admin scope combines
  with nothing. `username_pattern` combines with neither `username` nor an
  `applied-permissions/user` scope, and `groups_pattern` does not combine with a groups
  scope. Every other pair is accepted, so a user or a user pattern may come with a groups
  pattern. `resource_pattern` also refuses artifact entries in the mapping's own scope,
  because a token's artifact entries allow their union and a claim's entry would widen
  them. A mapping without `username` or `username_pattern` needs a groups scope or
  `groups_pattern`.
- **Patterns** ("Dynamic Mapping" and "Advanced Dynamic Mappings"). `username_pattern`,
  `groups_pattern` and `resource_pattern` name one claim as `{{claim}}`, followed by at
  most 15 more parts separated by `|`.
  - The first part is a regular expression, its whitespace dropped, in which `{{claim}}`
    is a capture group, and the claim must match it whole. So `jf{{actor}}jf|$1` turns
    `jfuser-f12jf` into `user-f12`, and a claim that does not match fails the request
    with 400.
  - A later part is an operator, `op:replaceAll("a","b")`, `op:replaceFirst` or
    `op:replaceLast`, which replace literal text; a regular expression the value must
    match; or a replacement. A replacement rebuilds the value from the groups of the
    latest expression, matched again against the current value. It reads `$1_$2` as
    Java does: group 1, `_`, group 2.
  - Of the parts that are not operators, one is an expression when another follows it
    and the one before it is not an expression; the others are replacements.
  - A username pattern must yield one name. A groups or resource pattern applies to
    every element of a list claim.
- **Pattern safety.** Patterns come from administrators, but claims come from external
  tokens.
  - A claim is only ever input. It is never compiled or expanded, so a `$` in it stays
    text.
  - A claim value, and every value a step produces, is at most 1024 bytes. A list claim
    has at most 100 elements and a pattern at most 16 parts. Go's regular expressions
    run in linear time, so these bound the work of one exchange.
  - A user or group name with a control character, or a group name with a quote, fails
    the request. The finished scope must read back as itself, within the 500-character
    scope limit, so no claim can add a scope entry.
- **Resource scope** ("Add a Resource Scope to an Identity Mapping"). Each value of
  `resource_pattern` becomes one artifact entry. A bare repository key is normalised to
  `artifact:<repo>/**:r`, and any other value must be one `artifact:` entry. Its
  repository must be named exactly, without `*` or `?`. A wildcard repository, a second
  entry or another resource type fails the request with 400. The entries only narrow:
  like every artifact entry they intersect the permissions of the token's user and
  groups ([0006](0006-authorization-native-permission-model.md)), so a claim never
  reaches a repository the mapping's user or groups cannot. That is why a mapping with
  `resource_pattern` needs a user or groups as well.
- **The token.** Its lifetime is `token_spec.expires_in`, one hour when unset, at most
  24 hours. Its scope is the mapping's `scope`, then the groups and artifact entries its
  patterns yield, then `applied-permissions/user` whenever the mapping names a user. So a
  token with groups and a user also acts through the auto-join groups ("Transient Users
  in OIDC Token Exchange"). A project roles scope takes no user scope (JA-19609), and an
  admin scope needs none. A named or patterned user may not exist: the token is marked
  `transient`, and authenticates as the stored user of that name when there is one,
  otherwise as a transient user in the auto-join groups. A mapping without a user
  issues for the synthetic subject `oidc-<provider>-<mapping>`, marked `synthetic`,
  which is never a stored user and acts through its scope's groups alone; such a
  mapping must therefore name groups only. A token an administrator mints for a name that
  is no stored user is marked `transient` too (**Token API** above). Every other token
  still needs a stored, enabled user. The description records the provider, the mapping
  and the request's
  CI fields; `audience`, `application_key` and the `gh_*` fields are accepted and
  ignored.
- **Statuses.** 400 for a malformed request, an unknown provider or mapping, no
  matching mapping, or a matching mapping whose patterns refuse the claims; 401 for a
  token that does not verify, including when the provider's keys cannot be fetched,
  which is logged at WARN. Management answers 404 for an unknown provider or mapping,
  400 for an invalid body, a combination the table refuses, a pattern that cannot be
  read, or a name in use.

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
  `secondsToUnlock` (default 1) is stored and returned only: a lock lasts until an
  administrator lifts it.
- **The temporary login suspension** (`internal/auth/suspension.go`, #408) is the second
  defence, independent of the lock. From the second failed Basic login in a row for a user
  name (`maxLoginDelayIncorrectAttempts`), every Basic login with that name answers `403`
  "This request is blocked due to recurrent request failures, please try again in N
  second(s)" for `baseLoginDelayMillis` (1000), the right password included; each later
  failure doubles the delay up to `maxLoginDelayMillis` (60000).
  - The check runs before the user is read or any password hashed, and an unknown name is
    suspended alike, so the `403` says nothing about which names exist.
  - Attempts refused while suspended are not counted, toward the suspension or the lock, so
    no stream of requests stretches one suspension past the maximum.
  - A login whose password verifies clears the failures, and they are forgotten a day after
    the last one. Tokens are never suspended.
  - The state is per node and in memory, as JFrog documents it, bounded at 10,000 names.
  - `clientLoginSuspensionEnabled` false, or a zero delay, turns it off.
- **A password expires** when an administrator expires it (whatever the policy) or
  when the expiration policy is enabled and the password is older than
  `passwordMaxAge` days. Age counts from the recorded change, so passwords set before
  it was recorded never expire on their own. Expiry is reported only after the
  password verifies, as 403 with "Password has expired" on every route but the user's own
  password change. That is the answer of Artifactory's entry point to an expired password,
  which it gives a locked account too (**[I]**, from OSS 6.20; research 05 lists the
  evidence for [#216](https://github.com/weaveplatform/artifactweave/issues/216)).
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
  expired password through for two requests: `PUT /access/api/v2/users/{own name}/password`
  and the legacy `POST /artifactory/api/security/users/authorization/changePassword`. The
  legacy change names its user in the body, so its handler holds an expired password to its
  own name, administrator or not. Without these requests, a user whose password expired could
  not choose a new one.
- **The legacy password change** proves itself with the current password, which must verify
  for every caller, an administrator included; an expired one verifies. A wrong one counts
  against the lock policy as a wrong login does, since the request is already authenticated
  and could otherwise guess without limit. The policy applies to the new password.
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
`POST /jfconnect/api/v1/backoffice/metrics/log` answer 2xx and discard a body of up to
1 MiB, and 413 to a longer one ([0016](0016-observability-and-health.md#decision)).

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
([#382](https://github.com/weaveplatform/artifactweave/issues/382)). The points below record
the rest; those marked "7.x differs" still do.

**The default token lifetime.** JFrog's sources disagree:
- the [access-tokens page](https://docs.jfrog.com/administration/docs/access-tokens)
  (updated 2026-07-06) says "The default expiry is 3600 seconds";
- the Access configuration reference gives `token.default-expiry: 31536000`
  ([supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)),
  and the [recommended configurations](https://docs.jfrog.com/administration/reference/recommended-configurations)
  page says "Default value: 1 year";
- the JFrog CLI's `jf atc --expiry` help says the platform default is "1 year by default"
  (jfrog-cli `utils/cliutils/commandsflags.go`, `atcExpiry`).

Maintainer decision (2026-10-18): the default is one year, following the configuration
reference and the CLI. A token created through `POST /access/api/v1/tokens` without
`expires_in` lives one year, and `default_expiry` reads 31536000 until an administrator
changes it
([#406](https://github.com/weaveplatform/artifactweave/issues/406)).
The access-tokens page's 3600 seconds conflicts with this, and the live check on
[#216](https://github.com/weaveplatform/artifactweave/issues/216) settles which one 7.x applies. The
legacy route, the OIDC exchange and the registry token keep their own one-hour defaults.

**Tokens:**
- A non-administrator's `expires_in: 0` answers 403, "The requested token details are
  forbidden". That is the status and message createtoken gives a refused request, and its
  field note reads "0 for non-expirable (admin only)"
  ([createtoken](https://docs.jfrog.com/administration/reference/createtoken);
  [#406](https://github.com/weaveplatform/artifactweave/issues/406)).
  The recommended configurations page says that while `token.max-expiry` is at its default 0
  "users can set the `expires_in` parameter to any value"
  ([recommended configurations](https://docs.jfrog.com/administration/reference/recommended-configurations)).
  Whether 7.x refuses it at all is therefore inferred **[I]**, and on
  [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- A refreshable token is revocable, and listed, whatever its lifetime
  ([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue):
  "created with `force_revocable: true` or `refreshable: true`";
  [#406](https://github.com/weaveplatform/artifactweave/issues/406)).
- Revoking a non-revocable token by id answers 400 (KB
  [000005622](https://jfrog.com/help/r/artifactory-how-to-troubleshoot-failed-to-revoke-tokens-when-trying-to-revoke-a-token-through-the-ui);
  [#406](https://github.com/weaveplatform/artifactweave/issues/406)).
- Revoking a non-revocable token by value answers 404, "Token not found or not revocable"
  ([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue);
  [#406](https://github.com/weaveplatform/artifactweave/issues/406)).
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
- A token an administrator mints for a user that does not exist authenticates as a transient
  user, and a groups-scoped one acts with its groups' permissions
  ([access tokens](https://docs.jfrog.com/administration/docs/access-tokens), "Support
  Authentication for Non-existing Users";
  [#406](https://github.com/weaveplatform/artifactweave/issues/406)).
  Under `applied-permissions/user` the transient user also acts through the auto-join groups,
  as the page's table gives it: "Explicitly mapped groups plus any local Auto Join groups".
  The page says such a user "exists only for the duration of that session and is not saved
  to the database", but not how the user API answers for it or how its tokens are revoked.
  The 404 from the user API, nothing revoked by deleting the name, and revocation through the
  token API as for any other token are inferred **[I]**, and on
  [#216](https://github.com/weaveplatform/artifactweave/issues/216).

**Account policies and seeded principals:**
- Failed Basic logins also suspend the account for a moment, answering 403 for a delay that
  grows to at most 60 seconds, and `seconds-to-unlock` defaults to 1
  ([security configuration](https://docs.jfrog.com/administration/docs/security-configuration),
  "Temporary Login Suspension";
  [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)).
  artifactweave matches both (**Account policies** above,
  [#408](https://github.com/weaveplatform/artifactweave/issues/408)). The doubling, the day
  after which failures are forgotten, the 403's message and leaving tokens alone are
  inferred, and on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- Since 7.84.3 `anonymous` is in neither the `Anything` nor the `Any Remote` permission
  ([permissions](https://docs.jfrog.com/administration/docs/permissions)), and artifactweave
  seeds both that way. Decision
  [0006](0006-authorization-native-permission-model.md#seeded-permissions-383) records what
  the seed grants.

**OIDC token exchange** ([research 21](../21-oidc-token-exchange.md#3-matching-and-the-token-issued)
section 3 describes each point, from
[OpenID Connect integration](https://docs.jfrog.com/administration/docs/openid-connect-integration)).
These match 7.x since [#409](https://github.com/weaveplatform/artifactweave/issues/409), as
**OIDC token exchange** above describes:
- A mapping that resolves a username adds `applied-permissions/user`, so the token acts
  through its named groups and the auto-join groups; from 7.133.10 not with a project roles
  scope (JA-18771 in
  [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed),
  JA-19609 in
  [7.133.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-713310-self-managed)).
- One mapping may combine a user with a groups pattern, and a user pattern with a groups
  pattern. It may not combine groups with a groups pattern, a user with a user pattern, or
  admin with anything (the page's combination table).
- A two-part pattern matches its template against the claim, the claim's place being a
  capture group, and a claim that does not match fails the request ("Dynamic Mapping").
- From 7.130.0 a pattern may chain more than three parts and use `op:replaceAll`,
  `op:replaceFirst` and `op:replaceLast` ("Advanced Dynamic Mappings").
- A Resource Scope mapping turns a claim such as `jfrog_scope` into an `artifact:` scope,
  and a bare repository name becomes `artifact:<repo>/**:r` (SaaS
  [7.172.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71721-saas)).
- With no `project_key`, "all identity mappings compete equally based on priority and
  creation time", a project's mappings included ("Identity Mappings").

Two choices go further than the page. First, the resource scope's entries narrow the user's
and groups' permissions, as every artifact entry does here
([0006](0006-authorization-native-permission-model.md)). The
[create-scoped-token](https://docs.jfrog.com/administration/reference/create-scoped-token)
page says a resource scope needs no permissions defined beforehand, but a claim from an
external token never grants anything on its own here. Second, a claim naming a wildcard
repository, another resource type or several entries fails the request. The details no
source states are inferred and listed on
[#216](https://github.com/weaveplatform/artifactweave/issues/216):
- the field that carries the pattern, `token_spec.resource_pattern`;
- the template read as a regular expression with its whitespace dropped, which the page's
  `({{actor}}) @jfrog.com|$1` example needs, and limited to one claim;
- which parts of a long chain are expressions and which replacements, operators replacing
  literal text, and `$1_$2` read as Java reads it;
- the table's "User" being the `username` field or an `applied-permissions/user` scope;
- another project's mappings not being tried when the request names a project;
- the bounds on claims and patterns, and the refusal of over-broad resource claims.

## Constraints

- The signing key is sealed at rest ([0017](0017-sealed-columns-and-master-key.md)) and
  rotation keeps the previous key for verification until the longest token expiry.
- Token scopes are evaluated after the permission model, never instead of it.
- Anonymous access is off by default.

## Verification

- Token issue, refresh, revoke and reference-token tests through the Access API.
- A test that every issued JWT payload decodes with `base64.RawStdEncoding`.
- `jf c add` and `jf rt access-token-create` in the acceptance suite.
- Transient users, the admin scope, the default lifetime and the revocation statuses (#406):
  `TestTransientUserToken`, `TestRevokeNonRevocable`, `TestDefaultExpiryApplies`, the status
  table, the matrix's `tok-transient` and `tok-admin-scope` principals, and `tokens.feature`.
- Token creation with a password: `TestAccessTokenWithPassword`, the status table and the
  route by principal matrix at the default, and `basic_auth_tokens.feature`, where the Access
  route refuses the password, the legacy route and `jf c add` with a password still work, and
  the token from the legacy route creates one through the Access route.
- `artifactory_scoped_token` and `platform_permission` Terraform acceptance.
- OIDC: `internal/service/oidc` and `internal/auth/idtoken` tests against a fake issuer
  (`idtokentest`), including every dynamic mapping example on the integration page;
  `TestOIDCDynamicMappings`, which follows a narrowed token to what it may read;
  `oidc.feature`, where the real `jf eot` exchanges a token and downloads with it, also
  through a dynamic mapping narrowed to one repository;
  `deploy/artifactweave/terraform/oidc` in the `terraform` job.
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
