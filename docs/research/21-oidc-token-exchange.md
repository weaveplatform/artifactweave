# OIDC token exchange for CI workload identity

Checked against: Artifactory 7.161.32 (self-managed; SaaS 7.174.0); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#6-artifactory-7x-check) records it.

Research baseline: **2026-10-14**.

Access lets a CI system trade the ID token its platform signs (GitHub Actions, GitLab,
Azure DevOps, Jenkins and others) for a short-lived JFrog access token, so a pipeline
needs no stored JFrog secret. Decision [0019](decisions/0019-human-authentication-tiers.md)
puts the exchange in v1; decision [0005](decisions/0005-authentication-and-tokens.md)
records how artifactweave implements it. This report collects the wire behaviour the
clients depend on. The configuration and mapping shapes the Terraform provider sends are
also in [research 08](08-terraform-providers.md) and [research 17](17-undocumented-shapes.md).

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented by JFrog at the cited URL, read on 2026-10-14 |
| **[S]** | seen in source code at the cited commit |
| **[I]** | inferred from the documented facts; not stated by any source |

## 1. The operations

- **[D]** Providers: `POST /access/api/v1/oidc` creates and answers 200;
  `GET /access/api/v1/oidc` lists; `GET|PUT|DELETE /access/api/v1/oidc/{name}`. The
  fields are `name`, `issuer_url`, `audience`, `description`, `provider_type`,
  `token_issuer`, `enable_permissive_configuration`, `use_default_proxy` and
  `azure_app_id` (the cached `createoidcconfiguration` definition). "Unless otherwise
  specified, all APIs require admin privileges."
- **[D]** Identity mappings: `POST|GET /access/api/v1/oidc/{provider_name}/identity_mappings`,
  `GET|PUT|DELETE .../identity_mappings/{mapping_name}`. A mapping has `name`,
  `description`, `claims` (a free-form object), `priority` and `token_spec`
  (`username`, `scope`, `audience`, `expires_in`, `username_pattern`, `groups_pattern`,
  `self_revocable`); the response adds `project_key` (`createoidcidentitymapping`).
- **[D]** The exchange, `POST /access/api/v1/oidc/token`: request `grant_type`,
  `subject_token_type`, `subject_token`, `provider_name`, `project_key`,
  `application_key`, `identity_mapping_name`, `include_reference_token`,
  `provider_type`, `job_id`, `run_id`, `repo`, `revision`, `branch`; response
  `access_token`, `token_type`, `expires_in`, `issued_token_type`, `username`,
  `reference_token`. It answers 200, 400 (bad request) or 401 (bad credentials)
  (`oidctokenexchange`). The grant type is
  `urn:ietf:params:oauth:grant-type:token-exchange` and the subject token type
  `urn:ietf:params:oauth:token-type:id_token`
  (<https://docs.jfrog.com/administration/docs/openid-connect-integration>).
- **[D]** Licensing: Enterprise X or Enterprise+ (same page).

## 2. What the clients send and read

| Client | Request | Reads | Source |
|---|---|---|---|
| jfrog-client-go `ExchangeOidcToken` | the documented fields plus `audience`, sent with no credentials; 200 required | `access_token`, `username`, `issued_token_type`, `scope`, `expires_in`, `token_type`, `refresh_token` | **[S]** `access/services/accesstoken.go`, `auth/authutils.go` at jfrog-client-go `6053434e9301` |
| `jf eot <provider> <token>` (jfrog-cli 2.75+) | `provider_type` is `GitHub`, `Azure` or `GenericOidc` (default `GitHub`); `--oidc-audience` fills `audience`; CI variables fill `job_id`, `run_id`, `repo`, `revision`, `branch` | prints `{"AccessToken":…,"Username":…}` | **[S]** `general/token/oidctokenexchange.go` at jfrog-cli-core `b47892ded3a0`; `general/token/cli.go` at jfrog-cli `8ea6691fe701` |
| setup-jfrog-cli | runs `jf eot … --url … [--oidc-audience]`; for older CLIs posts the exchange itself with `project_key` (possibly empty) and `gh_job_id`, `gh_run_id`, `gh_repo`, `gh_revision`, `gh_branch` | requires both `AccessToken` and `Username` to be non-empty; the fallback reads `errors` from a failure and derives the user from the JWT `sub` after its last `/` | **[S]** `src/oidc-utils.ts` at setup-jfrog-cli `1e5845068719` |
| jfrog/platform 2.2.11 `platform_oidc_configuration` | sends `provider_type` as `Generic OpenID Connect`, `GitHub`, `GitHub Enterprise` or `Azure`; always sends `organization` (empty for generic) and `use_default_proxy` | maps `Generic OpenID Connect` back to `generic`; for GitHub types reads `organization`, falling back to `token_issuer`; refreshes `enable_permissive_configuration` for GitHub only when in state | **[S]** `pkg/platform/resource_oidc_configuration.go` at terraform-provider-platform `1dd3a35263a3` |
| jfrog/platform 2.2.11 `platform_oidc_identity_mapping` | always sends `priority` (≥1), `claims`, `token_spec.scope`, `token_spec.audience` (default `*@*`) and `token_spec.expires_in` (default 60); `username` conflicts with both patterns; group scopes in quotes, `applied-permissions/groups:"readers"` | reads `claims` back as JSON for `claims_json`, and `token_spec` fields only when non-empty; 404 on read removes the resource | **[S]** `pkg/platform/resource_oidc_identity_mapping.go` at the same commit |

## 3. Matching and the token issued

From <https://docs.jfrog.com/administration/docs/openid-connect-integration>:

- **[D]** "If the user does not exist, they become a transient user for that request"
  and "automatically inherit all groups configured with Automatically Join New Users".
  The platform issues the token "without validating whether that user actually exists".
- **[D]** "If you need strict group-scoped permissions, leave the Username field blank
  in your identity mapping. The token will use a synthetic subject instead, preventing
  Auto Join group expansion." The synthetic subject's form is not documented.
- **[D]** Claims may use wildcards (`"repository": "jfrog/*"`,
  `"repository": "jfrog/jfrog-cli/**"`), several values, and dotted paths into nested
  claims (`custom_claims.department.group`, Artifactory 7.161.5+). How `*` differs from
  `**`, and whether a list matches any element, is not stated.
- **[D]** Priority: with a `project_key`, the project's mappings are tried before global
  ones; lower numbers first; equal priorities go to the mapping created first. "When no
  `project_key` is supplied, all identity mappings compete equally based on priority and
  creation time", a project's mappings included ("Identity Mappings").
- **[D]** The expiration time is "Limited to 24 hours, or 1440 minutes".
- **[D]** A mapping's token may carry several scopes, user and group scopes together,
  from 7.104.5 (SaaS 7.104.2) ([7.104.5 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)).
- **[D]** From 7.161.15 (SaaS 7.161.5) administrators can stop project admins seeing
  global identity mappings ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)). SaaS 7.172.1 ([release notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71721-saas)) adds a Resource Scope
  dynamic mapping; it is not yet in self-managed 7.161.32. The integration page dates it from
  7.161.5: a claim such as `jfrog_scope` becomes the token's resource scope, and a bare
  repository name is normalised to `artifact:<repo>/**:r`.
- **[D]** Dynamic mapping (7.95+), "Dynamic Mapping": a pattern names its claim as
  `{{claim}}`, and "If the claim is not found, the token request fails."
  - `{{claim}}` alone takes the claim's value as it is.
  - `template | replacement` matches the template against the claim. `{{claim}}` stands for a
    capture group, and the text around it must match literally. The replacement builds the
    result from `$0`, the whole match, and `$1`, the group: `jf{{actor}}jf|$1` turns
    `jfuser-f12jf` into `user-f12`, and `{{actor}} | hub$0jfrog` turns `user` into
    `hubuserjfrog`. A claim that does not match fails the request:
    `jf{{mail}}jfrog.com|$0` fails for a mail that does not start with `jf`.
  - `{{claim}} | regex | replacement` matches the claim against a regular expression.
  - A groups pattern "is applied to all groups and adds the pattern to the group scope".
- **[D]** Advanced dynamic mappings (7.130.0+, "Advanced Dynamic Mappings"): a pattern may
  chain more than three parts, and a part may be `op:replaceAll("a","b")`,
  `op:replaceFirst(…)` or `op:replaceLast(…)`. For example
  `{{actor}} | op:replaceAll("-","_") | ^([^_]+)_([^_]+)_([^_]+)_mac$ | $1_$2_$3_artifactory_cicd`.
- **[D]** A mapping that resolves a username issues "a token with the
  `applied-permissions/user` scope", which is added to any groups scope ("Transient Users in
  OIDC Token Exchange"). User only acts through the auto-join groups, groups only through the
  named groups, and user with groups through both. 7.133.3 fixed a token from group mapping
  with dynamic user mapping that lacked the user scope (JA-18771,
  [release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)).
  From 7.133.10 a mapping with a project roles scope no longer gets it (JA-19609,
  [release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-713310-self-managed)).
- **[D]** Which scopes one mapping may combine (the page's table under "Identity Mappings"):

  | Scope | Combines with |
  |---|---|
  | Admin | nothing |
  | User | Groups, Groups Dynamic, Resource Dynamic |
  | Groups | User, User Dynamic, Resource Dynamic |
  | User Dynamic | Groups, Groups Dynamic, Resource Dynamic |
  | Groups Dynamic | User, User Dynamic, Resource Dynamic |
  | Resource Dynamic | every scope but Admin |

  The table marks each cell with an icon; `30c2518e…svg` is a cross and `a8f4274b…svg` a
  tick.
- **[D]** GitHub: `organization` is mandatory unless `enable_permissive_configuration`
  is true (7.110.1+), and case sensitive. Token issuer is needed "only when the OIDC
  provider URL differs from the token's issuer". Azure's `azure_app_id` is "required
  only for custom signing keys".
- **[D]** The errors for a token that matches no mapping, or does not verify, are not
  documented beyond the 400 and 401 of the operation.

## 3a. Exchanged tokens at the Docker registry

Researched for #248 on 2026-10-16: whether a token from the exchange is meant to reach
Artifactory's Docker registry, and as whom.

- **[S]** `setup-jfrog-cli` declares the outputs `oidc-token` ("JFrog OIDC token generated
  by the Setup JFrog CLI when setting oidc-provider-name") and `oidc-user` ("JFrog OIDC
  username from the OIDC token") (`action.yml` at setup-jfrog-cli `1e5845068719`).
- **[D]** Its README's OIDC examples configure the CLI with `oidc-provider-name` alone, then
  run `jf docker build` and `jf docker push` to a `docker-local` repository
  (`README.md` at the same commit).
- **[S]** `jf docker push` and `pull` log the Docker client in before running it: the
  password is the configured access token, and the user name is the configured one or,
  when there is none, the one extracted from the access token
  (`ContainerManagerLogin`, `artifactory/commands/ocicontainer/containermanager.go`, lines
  248-257, at jfrog-cli-artifactory `4a0f3827cfa0`). With an exchanged token that is
  Basic with the exchanged user and token, from which the Docker client fetches a registry
  token at the realm.
- **[S]** The JFrog Terraform providers' `oidc_provider_name` posts
  `{grant_type: "urn:ietf:params:oauth:grant-type:token-exchange", subject_token_type:
  "urn:ietf:params:oauth:token-type:id_token", subject_token, provider_name}` to
  `/access/api/v1/oidc/token`, taking the subject token from `TFC_WORKLOAD_IDENTITY_TOKEN`
  (Terraform Cloud's workload identity, not GitHub's), reads only `access_token`, and uses
  it as the Bearer credential (`OIDCTokenExchange`, `util/util.go`, and `client/client.go`
  `SetAuthToken`, at terraform-provider-shared `adad42471470`). They never reach the
  registry.
- **[D]** As section 3 records, the exchanged token's user may be transient, with its
  permissions coming from the auto-join groups, or a synthetic subject acting through its
  groups scope.
- **[I]** Artifactory therefore authorizes a `docker login` and the registry token behind
  it as it authorizes the exchanged token itself: the same user, the same scope. How it
  binds a registry token to the exchanged one, and whether revoking the exchanged token
  ends registry tokens already issued, is not documented; artifactweave binds them and
  revokes them together, the more secure reading (decision 0005). Both points are on
  #216 for the live capture.

## 4. Inferred

- **[I]** GitHub's organisation check compares the token's `repository_owner` claim,
  the claim GitHub uses for the owning organisation.
- **[I]** Azure's application signing keys are fetched with `?appid=` on the JWKS URI,
  as Microsoft documents for applications with custom signing keys.
- **[I]** `*` stops at `/` and `**` does not, by analogy with the two examples above and
  with Artifactory's Ant-style repository patterns.

## 5. Versions at the research baseline

| Component | Version |
|---|---|
| JFrog Access docs | read 2026-10-14 |
| JFrog CLI | 2.126.0 (acceptance) |
| jfrog/platform Terraform provider | 2.2.11 |
| go-jose | v4.1.5 |
| setup-jfrog-cli | `1e5845068719` (section 3a) |
| jfrog-cli-artifactory | `4a0f3827cfa0` (section 3a) |
| terraform-provider-shared | `adad42471470` (section 3a) |

## 6. Artifactory 7.x check

Audited on 2026-10-18 against Artifactory 7.161.32 (SaaS 7.174.0) with the KB, Jira and
client sources ([#378](https://github.com/weaveplatform/artifactweave/issues/378)). The
integration page was read as `.md`, updated 2026-10-13.

**Confirmed for 7.x:** the operations and shapes of section 1, what the clients send and read
(section 2), transient users and the synthetic subject, nested claims, the 24-hour limit and
the order of mappings with a `project_key`.

**Corrected here:** section 3 left out how a two-part pattern matches, the 7.130.0
operators, the user scope that a resolved username adds, which scopes one mapping may
combine, and how mappings compete without a `project_key`.

**Where artifactweave differs:** on those five points and the Resource Scope mapping.
Decision 0005's [7.x check](decisions/0005-authentication-and-tokens.md#artifactory-7x-check)
lists them; [#409](https://github.com/weaveplatform/artifactweave/issues/409) fixes them.

**Unconfirmed for 7.x**, for the live check on
[#216](https://github.com/weaveplatform/artifactweave/issues/216):
- `*` and `**` in claims (section 4);
- whether a `token_spec.expires_in` over 24 hours is refused or capped;
- what `self_revocable` changes;
- whether an exchanged token shorter than six hours can be revoked. 7.133.3 fixed revoking one
  with a project roles scope (JA-18290,
  [release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)).
