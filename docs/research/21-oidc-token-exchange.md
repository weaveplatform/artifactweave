# OIDC token exchange for CI workload identity

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
  ones; lower numbers first; equal priorities go to the mapping created first.
- **[D]** The expiration time is "Limited to 24 hours, or 1440 minutes".
- **[D]** Dynamic mapping (7.95+): `{{claim}}` takes a claim's value;
  `pattern | regex | replacement` rewrites it, `$0` the whole match and `$1` the first
  group; "If the claim is not found, the token request fails." A groups pattern "is
  applied to all groups and adds the pattern to the group scope".
- **[D]** GitHub: `organization` is mandatory unless `enable_permissive_configuration`
  is true (7.110.1+), and case sensitive. Token issuer is needed "only when the OIDC
  provider URL differs from the token's issuer". Azure's `azure_app_id` is "required
  only for custom signing keys".
- **[D]** The errors for a token that matches no mapping, or does not verify, are not
  documented beyond the 400 and 401 of the operation.

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
