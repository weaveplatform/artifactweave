# Native browser login contract audit (#262)

Audit date: **2026-11-19**. Local baseline: **abfb4c62bbd3c6c2ccb4bdd592789227a1bfa76f**.
This is contract research, not implementation or live Artifactory qualification.
**Client** means verified first-party source; **Vendor** means JFrog documentation or
an identified original capture; **Choice** means a proposed artifactweave contract.

## Scope and local starting point

[#262](https://github.com/weaveplatform/artifactweave/issues/262) requires optional,
server-rendered approval without JavaScript, CSRF protection, expiring sessions and P4
access tokens. Its [2026-10-19 maintainer addition](https://github.com/weaveplatform/artifactweave/issues/262#issuecomment-6278531716)
also requires Terraform `login.v1`, `ports`, PKCE, localhost authorization-code return
and token exchange without refresh tokens. npm alone does not complete this issue.

The ticket's request to amend a “no UI” decision is obsolete:
[ADR0019](decisions/0019-human-authentication-tiers.md) already permits native approval
before P39. [P21 task 1](../implementation/plan.md#p21-browser-and-enterprise-authentication)
names both clients. Tasks 2–3 separately own enterprise providers, account linking,
MFA and real identity-provider qualification. Completing local-user approval must not
claim those capabilities. The phase-level P20/P12 dependencies do not identify a missing
wire fact that prevents a bounded #262 implementation over existing local identities.

Read together: [R17](17-undocumented-shapes.md#npm-web-login),
[R25](25-npm.md#4-login-and-tokens), [R31](31-terraform.md#6-authentication-and-terraform-login)
and [R32](32-terraform-backend.md#19-terraform-login-without-loginv1).
R17's two-minute session, five-second poll, `sessionId`, `{token, username}`,
single-use delivery and expiry `401` are local design proposals, not captured JFrog
facts. Its old disabled `404` is not the existing enabled-route behavior: #262's
[audit comment](https://github.com/weaveplatform/artifactweave/issues/262#issuecomment-6251266266)
and [current status test](../../test/server/artifacts/npm/status_npm_auth_test.go)
record authenticated `405`; unauthenticated requests may fail earlier with `401`.

[session](../../internal/access/core/session/doc.go) and
[login](../../internal/access/adapters/http/login/doc.go) are placeholders. Existing
[middleware](../../internal/access/adapters/http/authn_middleware.go) rejects stale
credentials before public-route handling unless a route explicitly skips authentication.
[npm's exception](../../internal/artifacts/ecosystem/npm/http/login.go) covers legacy
`/-/user/`, not web start/done. [Terraform discovery](../../internal/artifacts/ecosystem/terraform/http/terraform.go)
already permits unauthenticated access and omits `login.v1`; adding an object requires
replacing its homogeneous `map[string]string` representation. R32's pasted-token
fallback is an interim path, not the required OAuth flow.

## Current qualification versions

| Required client | Repository pin and primary-source identity |
|---|---|
| npm 11 | [Node image](../../test/acceptance/clients/node.Dockerfile): Node **26.13.1**; its [bundled metadata](https://github.com/nodejs/node/blob/v26.13.1/deps/npm/package.json) identifies **npm 11.20.0**, with [npm-profile 12.0.2](https://github.com/nodejs/node/blob/v26.13.1/deps/npm/node_modules/npm-profile/package.json). |
| npm 12 | [Second image](../../test/acceptance/clients/node-npm12/Dockerfile): Node **24.22.3**, **npm 12.2.1**; its [lockfile](https://github.com/npm/cli/blob/v12.2.1/package-lock.json) pins npm-profile **13.0.1**. |
| Terraform | [Current recipe](../client-recipes/src/terraform.json) and [CI](../../.github/workflows/quality-gate.yml): **1.17.1**; [go.mod](https://github.com/hashicorp/terraform/blob/v1.17.1/go.mod) pins terraform-svchost **0.2.1** and golang.org/x/oauth2 **0.36.0**. |

The older 1.16.4/1.16.6 research and frozen P10 evidence retain their historical meaning;
they are not current #262 native run evidence. The current recipe/workflows provide no
OpenTofu executable pin. OpenTofu is supplementary here: its [official protocol](https://opentofu.org/docs/internals/login-protocol/)
describes the same discovery/public-client/PKCE contract, with `tofu-cli` as an example,
not a required server identifier. [OpenTofu v1.13.0 source](https://github.com/opentofu/opentofu/blob/v1.13.0/internal/command/login.go)
also uses S256 and `http://localhost:<port>/login`. This source comparison is not a
native OpenTofu compatibility claim or an added prerequisite for #262.

## npm web login: verified client contract

The two pinned [npm 11 profile implementation](https://github.com/nodejs/node/blob/v26.13.1/deps/npm/node_modules/npm-profile/lib/index.js)
and [npm 12 profile implementation](https://github.com/npm/cli/blob/v12.2.1/node_modules/npm-profile/lib/index.js)
agree on the following:

| Step | Required behavior and relevant limits |
|---|---|
| Start | `POST <registry>/-/v1/login`, JSON `{}`. A successful response must contain absolute HTTP(S) `loginUrl` and `doneUrl`. The profile code does not require one particular successful POST status. Choose `200` locally. |
| Open and poll | Browser opening and polling run concurrently. `GET doneUrl` disables caching and parses JSON on **every** response: pending `202` needs a JSON body, such as `{}`, not an empty body. |
| Pending | `202` repeats; `Retry-After` is converted numerically to seconds. Positive values delay polling; missing, zero, negative, non-numeric or HTTP-date values do not. No maximum poll count or overall approval deadline is imposed here. |
| Success | Exactly `200` with a truthy `token`. `username` is optional. Other statuses or malformed/missing token/URL shapes fail this protocol. |
| URLs | Ordinarily opaque. Both pins rewrite a `doneUrl` whose hostname is exactly `registry.npmjs.org` to the configured registry origin/prefix; use the actual artifactweave public origin instead. |

The outer `webAuth` error catch spans polling as well as the POST: HTTP errors
`400–499` and `500` become `ENYI`. Both pinned CLI wrappers
([11](https://github.com/nodejs/node/blob/v26.13.1/deps/npm/lib/utils/auth.js),
[12](https://github.com/npm/cli/blob/v12.2.1/lib/utils/auth.js)) catch it and attempt
legacy credentials, including with explicit `--auth-type=web`. Therefore disabling
the web feature or returning poll `401/403` need not make the whole CLI immediately
exit: it may prompt for legacy login. Acceptance must prove **no web credential is
issued**, not promise legacy authentication is disabled.

[Pinned transport](https://github.com/npm/cli/blob/v12.2.1/node_modules/make-fetch-happen/lib/remote.js)
does not retry POST; non-stream GET responses `408`, `420`, `429` and `5xx` are retryable.
[npm config defaults](https://github.com/npm/cli/blob/v12.2.1/workspaces/config/lib/definitions/definitions.js)
are two retries, factor 10, minimum 10 seconds, maximum 60 seconds, and a five-minute
HTTP request timeout. These are configurable transport limits, separate from `202`
polling and server approval expiry. Test hangs need an external process deadline.

[npm-registry-fetch](https://github.com/npm/cli/blob/v12.2.1/node_modules/npm-registry-fetch/lib/index.js)
can attach configured registry credentials; a fresh installation has none. Consequently
start/done must work while anonymous **package access** is off, and stale `.npmrc`
credentials must not preempt the new capability-based exchange. This is a bootstrap
requirement, not permission to approve anonymously. With `--browser=false`, the
[pinned opener](https://github.com/npm/cli/blob/v12.2.1/lib/utils/open-url.js) prints the
URL and skips the Enter/browser prompt; the
[login command](https://github.com/npm/cli/blob/v12.2.1/lib/commands/login.js) writes the
returned token to the configured registry's credentials after success.

## Terraform `login.v1`: discovery, PKCE and OAuth forms

[HashiCorp's protocol](https://developer.hashicorp.com/terraform/internals/login-protocol)
requires a `login.v1` object with `client`; `grant_types` defaults to `["authz_code"]`.
Advertising that grant requires `authz` and `token`; relative URLs resolve from the
discovery document. `ports` is optional in the public protocol, but the maintainer
explicitly requests it for #262. It describes a two-number **inclusive** range above
1023; absent ports allow 1024–65535. `client_id` identifies a public client and is not
authentication. Terraform does not retain refresh/expiry behavior: an expired bearer
requires another login.

The exact pinned wire is:

```text
GET https://<host>/.well-known/terraform.json       # bootstrap without a token

GET <authz>?response_type=code&client_id=terraform-cli
    &redirect_uri=http%3A%2F%2Flocalhost%3A<port>%2Flogin
    &state=<unpredictable-value>&code_challenge=<base64url-sha256-verifier>
    &code_challenge_method=S256

# After authenticated approval: browser redirect, code only, never an access token
Location: http://localhost:<port>/login?code=<one-use-code>&state=<exact-original>

POST <token>                                    # application/x-www-form-urlencoded
grant_type=authorization_code&client_id=terraform-cli&code=<one-use-code>
&redirect_uri=<exact-original-uri>&code_verifier=<original-verifier>

200 application/json
{"access_token":"<P4 token>","token_type":"Bearer","expires_in":<seconds>}
```

[terraform-svchost 0.2.1](https://github.com/hashicorp/terraform-svchost/blob/v0.2.1/disco/host.go)
also accepts an optional string-array `scopes`; omit it for this local flow.
Its [OAuth endpoint configuration](https://github.com/hashicorp/terraform-svchost/blob/v0.2.1/disco/oauth_client.go)
selects `AuthStyleInParams`. [oauth2's exchange](https://github.com/golang/oauth2/blob/v0.36.0/oauth2.go)
uses **`authorization_code`** on the token wire, unlike discovery's **`authz_code`**,
and sends no `state` there. Its [transport/parser](https://github.com/golang/oauth2/blob/v0.36.0/internal/token.go)
sends `client_id` in the form without a client secret or Basic authorization; no auth-style
probe/retry is needed. It accepts token responses in any `2xx` with a nonempty
`access_token`, JSON or form-encoded; non-`2xx`, OAuth `error`, or missing token fails.
Serve the standard `200` JSON success and `400` JSON OAuth error shape locally,
including `invalid_grant` for expired/reused code or failed verifier/bindings, as
[RFC6749 §§4.1–5](https://www.rfc-editor.org/rfc/rfc6749.html#section-4.1) specifies.

[Terraform 1.17.1 login source](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/command/login.go)
binds IPv4 `127.0.0.1`, but advertises **`http://localhost:<port>/login`**. Its random
selection is `[min,max)`, despite the documented inclusive interval; equal endpoints
make no attempt. Choose a nondegenerate range, e.g. `[10000,10010]`, and allow its
advertised endpoints. The callback checks exact `state` and a nonempty `code`, returns
`400` otherwise, and waits without an approval timeout. Successful code login stores
the access token, not the refresh token. An OAuth denial `error=access_denied&state=...`
has no code: this client returns `400` and continues waiting. Preserve standards-based
denial; show cancellation guidance and bound negative native tests externally.

## JFrog evidence and its boundary

**Vendor documentation:** [npm instructions](https://docs.jfrog.com/artifactory/docs/npm-repositories)
name `/artifactory/api/npm/<repo>/` and `npm login --auth-type=web`.
[JFrog's npm-v9 KB](https://jfrog.com/help/r/artifactory-how-to-fix-npm-v9-login-and-npmrc-auth-errors-in-artifactory)
places web-login support in 7.52. Original Jira
[RTFACT-29293](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29293) explains the
URL-pair and `202`/`200` contract, but its `$CREATE_TOKEN_API` URL is schematic;
[RTFACT-29487](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29487) reports 7.52.x's
web redirection interfering with npm 8 legacy login. Neither supplies an exact modern
start/done/denial capture or approval lifetime.

**Original capture:** the author's [2024-11-21 discovery response](https://github.com/aquasecurity/trivy/discussions/7982)
shows `client: terraform-cli`, `grant_types: [authz_code]`,
`authz: https://<host>/ui/terraform/oauth2/authorize`,
`token: https://<host>/artifactory/api/oauth2/token`, and **no `ports`**. The curl command
includes a bearer credential: it does not prove anonymous discovery. This is primary
evidence of that response, not a JFrog guarantee across current versions. Current
[JFrog Terraform/OpenTofu instructions](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories)
document `terraform`/`tofu login <hostname>`, clicking Approve, and access-token storage;
they do not document exact authorization/token request/error bodies. Reusing the
captured paths is a justified compatibility choice, not a new wire capture.

**Settings/timeout search:** no precise npm/Terraform native-approval enable property
or approval-session TTL was found in those docs, the two Jira records, or the
[System YAML reference](https://docs.jfrog.com/installation/docs/artifactory-system-yaml).
That reference's `frontend.session.timeMinutes: '30'` concerns frontend token refresh.
[OAuth SSO settings](https://docs.jfrog.com/administration/docs/oauth-sso) enable external
providers, not this native approval capability. [JFrog CLI login](https://docs.jfrog.com/integrations/docs/jf-login)
documents about five minutes/100 polls, another protocol; do not import that TTL.
[JFrog's npm token-expiry KB](https://jfrog.com/help/r/artifactory-how-to-modify-the-expiration-date-of-the-token-generated-by-the-npm-login-command)
does document `artifactory.artifactory.tokens.expiration.timeSecs` and a 30-day default:
that is **issued-token lifetime**, already mapped by local
[`npm.login_token_expiry`](../../internal/app/config/config.go), not approval-session
expiry. No vendor configuration name should be invented for #262.

## Required security lifecycle and bounded implementation choices

The following are **Choice** recommendations grounded in the above contracts. Settle
their concrete names, lifetimes and statuses within #262; no later enterprise provider
or P39 console is needed to make these decisions.

1. **Separate capabilities and states.** Use unpredictable, distinct browser-approval
   and npm polling secrets; store digests and immutable client/repository bindings.
   Possessing a poll handle must not authorize approval; discovering a browser URL
   must not reveal the token. Model `pending → approved → redeemed`, plus terminal
   `denied/expired/cancelled`. Terraform adds a one-use authorization-code stage.
   Concurrent approval/redemption must have one winner, with no duplicate minting.
2. **Separate clocks.** Define browser-session absolute/idle lifetime, pending approval
   TTL, authorization-code TTL and P4 token expiry independently. Two-minute pending
   TTL and numeric `Retry-After: 5` are reasonable R17 choices, not vendor facts.
   Expiry must be checked on every transition, including approval and redemption;
   cleanup alone cannot enforce it. Denial/expiry/logout must prevent later approval.
   Decide whether logout also revokes issued client tokens; a browser cookie alone
   cannot revoke them. Single-use delivery can require a fresh login after response
   loss; do not silently invent replayable token retrieval.
3. **Browser authentication and CSRF.** Serve escaped HTML with no JavaScript or
   third-party resources; GET only displays, POST authenticates/approves/denies/logs out.
   Bind CSRF tokens to the browser session and action, check request origin, rotate the
   session identifier after authentication, and use `HttpOnly`, suitable `SameSite`,
   path-scoped cookies with `Secure` on HTTPS. Require explicit consent showing user,
   client and npm registry. Native `state` and PKCE do not replace form CSRF protection.
   Prevent token/code leaks through caches, referrers and logs; reject framing.
   These implement [RFC9700's CSRF/redirect/leakage requirements](https://www.rfc-editor.org/rfc/rfc9700.html#section-4.7),
   with concrete cookie/header choices to be reviewed locally.
4. **PKCE and redirects.** Require `response_type=code`, the configured public client,
   valid `S256` challenge, and a validated callback before presenting approval.
   Bind code to client, exact redirect, approving identity, challenge/method and expiry;
   accept a 43-character base64url S256 challenge and a 43–128-character RFC-unreserved verifier;
   verify `BASE64URL(SHA256(code_verifier))` without padding and reject downgrade,
   missing/wrong verifier and replay ([RFC7636 §§4.3–4.6](https://www.rfc-editor.org/rfc/rfc7636.html#section-4.3)).
   For the pinned client allow only exact `http://localhost:<advertised-port>/login`,
   with no userinfo, fragment, attacker-controlled host or extra redirect path/query.
   Do not resolve arbitrary hostnames to decide whether they are loopback. Invalid
   callbacks fail locally, never redirect. General IP-literal native callbacks are a
   separate allowance if deliberately supported; [RFC8252 §§7.3–8](https://www.rfc-editor.org/rfc/rfc8252.html#section-7.3)
   explains loopback/public-client risks. OAuth success/denial echoes the original
   state; no access token travels in a redirect.
5. **Local identities and token scope.** Reuse
   [Basic authentication policy](../../internal/access/core/authn/authenticator.go),
   including disabled/internal-password policy, lock/suspension and password expiry.
   Do not accept a client-supplied username as consent identity or auto-provision a
   transient/federated identity. Recheck current identity/policy at approval and minting
   ([current principal checks](../../internal/access/core/authn/current.go)). Basic already
   records `PasswordRevision`; Current can check it inside the issuing transaction
   without retaining or rechecking the raw password. Reuse
   [P4 issuance](../../internal/access/core/token/token.go), finite expiry, revocation
   and `applied-permissions/user`; do not allow arbitrary scope, subject, audience,
   refresh grants or admin promotion through native input. Preserve
   [npm legacy safeguards](../../internal/artifacts/ecosystem/npm/http/user.go) against
   upgrading narrowed, registry or transient tokens. Force revocability as npm logout
   already requires. Registry-scoped `.npmrc` storage does **not** make a general P4
   token repository-restricted; state that limit and recheck repository permissions
   on normal use. User disable/deletion and token revocation must still take effect
   through [existing verification](../../internal/access/core/authn/token.go).
6. **Feature and route boundary.** Choose and document a local setting/default; when
   off, refuse start/authz/token and omit discovery `login.v1`. A disabled npm start
   `404` satisfies the issue but permits legacy fallback. Retain legacy login and
   Terraform's pasted-token/configured-token paths. Narrow anonymous middleware
   exceptions to bootstrap/capability exchanges; browser approval remains authenticated.
   Bound request sizes, pending-session population and request rates. Use the existing
   base-URL resolver with configured/trusted proxy policy; never derive approval/token
   URLs or trusted redirects from arbitrary query parameters or forwarded headers.

## Finite acceptance matrix

These are requirements for the implementation wave, **not runs performed by this audit**.
Use disposable client homes/configuration, captured sanitized requests, a controllable
clock for expiry and hard subprocess deadlines. Browser steps may use an HTTP form
driver; no browser JavaScript is required. Record exact executable versions.

| Case | Evidence needed |
|---|---|
| npm 11.20.0 and 12.2.1 success | Each real `npm login --auth-type=web --browser=false` with anonymous package access off: `{}` POST, displayed URL, at least one JSON `202`/numeric Retry-After, authenticated approval, one `200` token, persisted registry credential, successful protected npm request. |
| npm route and credential isolation | HTTP local/virtual npm registry routes; no/stale credentials can start; unknown/non-npm repository rejected; another registry/session/poll handle cannot approve or redeem this approval. |
| npm refusal and shape | Setting off, denied, expired, unknown and already-used handle never disclose a token; observe possible legacy prompts. JSON `202` body and missing/invalid token/URL handling have focused protocol checks. |
| npm retries and legacy regression | Confirm delayed pending poll, bounded transient GET retry, no duplicate POST session/mint, and existing explicit legacy login/logout still work; denied web flow is not mistaken for disabled legacy login. |
| Terraform 1.17.1 success | Real `terraform login <TLS-host>` using anonymous discovery, advertised nondegenerate ports, captured S256/state and exact `/login` callback, form exchange without secret, stored host credential, then a real authenticated command against an existing protected service. |
| Terraform off/fallback | No `login.v1` when off; existing `tfe.v2` pasted-token or manual credentials remain usable. Discovery still retains modules/providers/backend service entries. |
| PKCE and code binding | HTTP tests reject missing/invalid/plain challenge, wrong/missing verifier, other client/redirect, expired code, duplicate and concurrent exchange; only one valid redemption mints one token. |
| Callback and OAuth denial | Validate configured port edges/path, malicious/userinfo/non-loopback callbacks and state preservation; actual CLI rejects wrong state/missing code. Denial issues no code/token; externally cancel the waiting CLI. |
| Browser mutation security | Missing/other-session CSRF, cross-origin POST, GET approve/logout, session fixation, replay, hostile HTML inputs and framing fail; verify cookies, cache/referrer headers and no secrets in logs. |
| Account/policy changes | Wrong password, disabled/locked/suspended/expired account; identity disabled/deleted or policy changed between login, consent and redemption; no resulting usable credential. |
| Session and operational limits | Pending/browser/code expiry boundaries, logout/cancel, population/body limits, concurrent approve/deny/redeem and issuance failure; restart behavior is explicit and fails closed. |
| Scope/revocation | Token has intended local subject and user scope, finite configured lifetime and no refresh token; forbidden repository remains forbidden, logout/revocation and later user disable invalidate required credentials. |
| Proxy and base URL | Canonical URLs behind configured base URL, valid TLS and selected proxy policy; untrusted host/forwarded values cannot redirect token delivery. |

OpenTofu native runs may be added only with an explicit version pin and isolated home;
the matrix above does not claim them. It also does not qualify SAML, human OIDC/OAuth,
Crowd, HTTP-SSO, MFA, SCIM, shared HA sessions or the P39 console.

## Open facts versus decisions and prerequisites

- **Unresolved vendor wire facts:** current Artifactory's exact npm `loginUrl`/`doneUrl`,
  pending body/retry value, denial/expiry/replay statuses, anonymous/stale-credential
  bootstrap behavior, approval/code lifetimes and modern Terraform success/error
  captures. Obtain redacted, version-labelled captures for parity claims; the pinned
  client contract is sufficient to implement #262 while labelling local choices.
- **Decisions inside #262:** feature setting/default, browser routes and cookie policy,
  independent TTLs, port range, disabled/terminal status bodies, single-use response-loss
  semantics, session storage/restart behavior and browser logout versus token revocation.
  None requires waiting for an external enterprise identity provider. The concrete
  choices must be documented before claiming the matrix passes.
- **Implementation prerequisites:** implement the placeholder session/approval/PKCE
  core and HTTP adapter, integrate existing local authn/token services and stores,
  wire narrowly scoped bootstrap routes and mixed discovery values, and provide
  disposable native-client/TLS/form-driving acceptance support. These are deliverables
  of this wave, not evidence that later P21 provider tasks are complete. Multi-node
  session consistency/failover remains P35 work unless this implementation explicitly
  undertakes and qualifies it; a bounded single-node claim must say so.

Only this note was added. No client/server builds, native login runs, Docker changes,
cache changes, production changes or live vendor login captures were made.
