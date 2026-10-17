# 08: The JFrog Terraform providers

Checked against: Artifactory 7.161.32

The three JFrog providers are the GitOps client artifactweave must satisfy unchanged. This
document records what each provider calls at configure time, every resource and data
source, the endpoints and exact status codes each resource needs, and the version gates
that the reported Artifactory version unlocks.

Research baseline: **2026-10-12**. Sources: the provider sources on GitHub at the versions
below, the shared client library `jfrog/terraform-provider-shared`, and the Terraform
Registry documentation. Versions and dates match the GitHub release pages. Items marked
*(unverified)* lack a primary source.

| Provider | Version | Released | Source |
|---|---|---|---|
| `jfrog/artifactory` | v12.11.14 | 2026-09-11 | https://github.com/jfrog/terraform-provider-artifactory |
| `jfrog/platform` | v2.2.11 | 2026-08-25 | https://github.com/jfrog/terraform-provider-platform |
| `jfrog/project` | v1.9.9 | 2026-08-24 | https://github.com/jfrog/terraform-provider-project |
| shared client | main | | https://github.com/jfrog/terraform-provider-shared |

## Provider configuration and configure-time behaviour

Configuration: `url` (`JFROG_URL` / `ARTIFACTORY_URL`), `access_token`
(`JFROG_ACCESS_TOKEN`, sent as Bearer), `api_key` (deprecated, `X-JFrog-Art-Api`, ignored
when a token is set), `oidc_provider_name` (Terraform Cloud workload identity exchanged
through `POST /access/api/v1/oidc/token`). The shared client discards the URL path and
keeps only `scheme://host`, then prefixes `/artifactory`, `/access`, `/event`,
`/lifecycle` and `/worker` itself
(https://github.com/jfrog/terraform-provider-shared/blob/main/client/client.go), so
`url = "https://host/artifactory"` and `url = "https://host"` are equivalent.

At configure time (https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/provider/framework.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/util.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/provider.go):

1. `GET /artifactory/api/system/version`; only `version` is parsed.
2. Asynchronous `POST /artifactory/api/system/usage` with
   `{"productId":"terraform-provider-artifactory/<v>","features":[{"featureId":"Partner/ACC-007450"},{"featureId":"Terraform/<tfver>"}]}`;
   failures are ignored.
3. The platform provider additionally calls `GET /access/api/v1/system/version` and parses
   the `name` field as the Access version. A missing endpoint is only a warning, but
   `platform_lifecycle_stage`, `platform_lifecycle` (≥7.155.0) and newer OIDC fields
   (≥7.138.0 / 7.144.0) then misbehave.

There is **no licence check**. `CheckArtifactoryLicense` exists in the shared library with
no callers, the `check_license` provider attribute was removed, and only the
`platform_license` resource reads `/artifactory/api/system/license`.

User-Agent: `jfrog/terraform-provider-{artifactory,platform,project}/<version>`.

Version gates enforced per resource (`ValidArtifactoryVersion` and `CheckVersion`):

| Artifactory version | Resource or field |
|---|---|
| 7.53.1 | project environments on repositories |
| 7.72.0 | `platform_permission` |
| 7.84.3 | users through Access v2 (older servers use `/api/security/users`) |
| 7.90.10 | `platform_aws_iam_role` |
| 7.104.0 | Helm enforce-layout fields |
| 7.107.1 | multiple `environments` on a repository |
| 7.122.0 | remote Terraform repository fields |
| 7.128.0 | `roles` on groups |
| 7.146.0 | `customHttpHeaders` on remote generic repositories |
| Access 7.138.0 / 7.144.0 | newer OIDC configuration fields |
| Access 7.155.0 | `platform_lifecycle_stage`, `platform_lifecycle` |

artifactweave reports 7.155.0 or newer on both endpoints so no code path is silently
disabled (see [06-system-configuration-and-descriptor.md](06-system-configuration-and-descriptor.md)).

## `jfrog/artifactory`

### Repository resources

`artifactory_<local|remote|virtual|federated>_<type>_repository`, one resource per package
type. Each calls:

| Call | Expected status |
|---|---|
| `PUT /artifactory/api/repositories/{key}` (create) | any 2xx |
| `GET /artifactory/api/repositories/{key}` | 200 |
| `POST /artifactory/api/repositories/{key}` (update) | 2xx |
| `DELETE /artifactory/api/repositories/{key}` | **exactly 200** (framework resources); 200, 400 or 404 (sdkv2 resources) |
| `HEAD /artifactory/api/repositories/{key}` | 200 / 404 |
| `PUT` / `DELETE /access/api/v1/projects/_/attach/repositories/{repo}[/{project}]` | 204 |

`GetArtifactCount` (`GET /api/storage/{key}?list&deep=1&listFolders=0`) exists in the
code but is never called. There are no untyped `artifactory_local`, `artifactory_remote` or
`artifactory_virtual` resources in v12.

Package types per class (from `provider/resources.go` and `provider/framework.go`):

| Class | Types |
|---|---|
| local (40) | alpine, ansible, bower, cargo, chef, cocoapods, composer, conan, conda, cran, debian, docker_v1, docker_v2, gems, generic, gitlfs, go, gradle, helm, helmoci, hex, huggingfaceml, ivy, machinelearning, maven, nix, npm, nuget, oci, opkg, pub, puppet, pypi, rpm, sbt, swift, terraform_module, terraform_provider, terraformbackend, vagrant |
| remote (41) | aieditorextensions, alpine, ansible, bazel, bower, cargo, chef, cocoapods, composer, conan, conda, cran, debian, docker, gems, generic, gitlfs, go, gradle, helm, helmoci, hex, huggingfaceml, ivy, jetbrainsplugins, maven, nix, npm, nuget, oci, opkg, p2, pub, puppet, pypi, rpm, sbt, swift, terraform, vcs |
| virtual (33) | alpine, ansible, bower, chef, cocoapods, composer, conan, conda, cran, debian, docker, gems, generic, gitlfs, go, gradle, helm, helmoci, hex, ivy, maven, nix, npm, nuget, oci, p2, pub, puppet, pypi, rpm, sbt, swift, terraform |
| federated (37) | alpine, ansible, bower, cargo, chef, cocoapods, composer, conan, conda, cran, debian, docker, docker_v1, docker_v2, gems, generic, gitlfs, go, gradle, helm, helmoci, huggingfaceml, ivy, maven, npm, nuget, oci, opkg, puppet, pypi, releasebundles, rpm, sbt, swift, terraform_module, terraform_provider, vagrant |

In scope for artifactweave v1: local `docker_v2`, `generic`, `go`, `gradle`, `helm`,
`helmoci`, `maven`, `npm`, `nuget`, `oci`, `pypi`, `terraform_module`,
`terraform_provider`; remote and virtual `docker`, `generic`, `go`, `gradle`, `helm`,
`helmoci`, `maven`, `npm`, `nuget`, `oci`, `pypi`, `terraform`; federated accepted and
treated as local when the only member is the instance itself
([ADR 0003](decisions/0003-repository-model.md)). `terraformbackend` is rejected with 400.

Provider-specific quirks captured in [03-repository-configuration-model.md](03-repository-configuration-model.md):
`terraformType` is sent lower-case; `priorityResolution`, `downloadRedirect`,
`cdnRedirect`, `enableFileListsIndexing` and `blockPushingSchema1` are strings on local
repositories; several JSON keys (`dockerTagRetention`, `pyPIRepositorySuffix`,
`symbolServerUrl`, `terraformRegistryUrl`, `terraformProvidersUrl`, `dockerProjectId`,
`useNamespaces`, `hideUnauthorizedResources`, `resolveDockerTagsByTimestamp`,
`rejectInvalidJars`, `forceMetadataNameVersion`, `forceNonDuplicateChart`) are known only
from the provider structs.

### Other resources

| Group | Resource | Endpoints and required status |
|---|---|---|
| Artifacts | `artifactory_artifact` | `PUT /artifactory/{repo}/{path}` 201, `GET /artifactory/api/storage/{repo}/{path}` 200, `DELETE /artifactory/{repo}/{path}` 204 |
| | `artifactory_item_properties` | `GET /artifactory/api/storage/{repo}/{path}?properties` 200; `PATCH /artifactory/api/metadata/{repo}/{path}?recursiveProperties=0\|1` with `{"props":{...}}` 204; `DELETE /artifactory/api/storage/{repo}/{path}?properties=...` 204 |
| Users | `artifactory_user`, `artifactory_managed_user`, `artifactory_unmanaged_user`, `artifactory_anonymous_user` | ≥7.84.3: `POST /access/api/v2/users` 201, `GET/PATCH/DELETE /access/api/v2/users/{name}` 200/200/204, `PATCH .../groups`; otherwise `PUT/GET/POST/DELETE /artifactory/api/security/users/{name}`. `artifactory_anonymous_user` reads the seeded `anonymous` user |
| Groups | `artifactory_group` | `PUT /artifactory/api/security/groups/{name}` **201** (create and update-with-members), `GET ...?includeUsers=true\|false` 200, `POST` 2xx, `DELETE` 200 |
| Permissions | `artifactory_permission_target` (deprecated in favour of `platform_permission`) | `/artifactory/api/v2/security/permissions/{name}`: POST 201, PUT 200, GET 200, HEAD 200/404, DELETE 200 |
| Tokens | `artifactory_scoped_token` | `POST /access/api/v1/tokens` **200**; reads `token_id` from the response; sends `project_key` even when `""`; never sends `force_revocable`; `GET /access/api/v1/tokens/{id}` 200, 404 removes from state; `DELETE` 200 |
| | `artifactory_api_key` (deprecated) | `POST /artifactory/api/security/apiKey` **201**, `GET` 200, `PUT` 200, `DELETE` 200 |
| | `artifactory_access_token` | removed in v12 |
| Keys | `artifactory_keypair` | `POST /artifactory/api/security/keypair/` **201**; `GET .../{name}` 200 returning `{pairName,pairType,alias,publicKey}`; `DELETE` 200 |
| | `artifactory_certificate` | `POST /artifactory/api/system/security/certificates/{alias}` 200 (PEM, `text/plain`), `GET .../certificates` 200 (`fingerPrint`, matched case-insensitively), `DELETE` 200 |
| | `artifactory_distribution_public_key` | `POST /artifactory/api/security/keys/trusted` **201**; `GET` 200 `{"keys":[{kid,alias,fingerprint,key,issued_on,issued_by,valid_until}]}`; `DELETE .../{key_id}` 204 or 200 |
| Security config | `artifactory_general_security` | YAML `PATCH /artifactory/api/system/configuration` 200; reads `GET` XML |
| | `artifactory_password_expiration_policy` | `GET/PUT /artifactory/api/security/configuration/passwordExpirationPolicy` 200; decodes `enabled` as bool and `passwordMaxAge` as int |
| | `artifactory_user_lock_policy` | `GET/PUT /artifactory/api/security/userLockPolicy` 200 |
| | `artifactory_vault_configuration` | `GET /access/api/v1/vault/configs`, `PUT/DELETE .../{name}`; 501 in v1 |
| | `artifactory_global_environment` | `POST /access/api/v1/environments` **201** `{name}`; `GET` 200; `POST .../{name}/rename` 200; `DELETE` 204 or 404 |
| System config (YAML PATCH) | `artifactory_backup`, `artifactory_mail_server`, `artifactory_property_set`, `artifactory_proxy`, `artifactory_repository_layout`, `artifactory_ldap_setting`, `artifactory_ldap_group_setting`, `artifactory_oauth_settings`, `artifactory_trashcan_config`, `artifactory_saml_settings` (deprecated) | `PATCH /artifactory/api/system/configuration` with `application/yaml` 200 (sections `backups`, `mailServer`, `propertySets`, `proxies`, `repoLayouts`, `security.ldapSettings`, `security.ldapGroupSettings`, `oauthSettings`, `trashcanConfig`); reads `GET /artifactory/api/system/configuration` (XML); `artifactory_oauth_settings` reads `GET /artifactory/api/oauth` (JSON) |
| Access LDAP | `artifactory_ldap_setting_v2`, `artifactory_ldap_group_setting_v2` | `POST/PUT /access/api/v1/ldap/settings/`, `GET/DELETE .../{id}`; `POST/PUT /access/api/v1/ldap/groups/`, `GET/DELETE .../{id}`, `POST .../{name}/refresh`; later phase |
| Retention | `artifactory_package_cleanup_policy` | `POST /artifactory/api/cleanup/packages/policies/{key}` 201 (`enabled` omitted on create), `POST .../enablement` 200 only when true, `GET` 200, `PUT` 200 then enablement, `DELETE` **200**; ≥7.111.2 for day-based fields |
| | `artifactory_archive_policy` | `/artifactory/api/archive/v2/packages/policies/{key}[/enablement]` (≥7.102.0); 501 |
| | `artifactory_release_bundle_v2_cleanup_policy` | `/artifactory/api/cleanup/bundles/policies/{key}[/enablement]` (≥7.104.2); 501 |
| Replication | `artifactory_local_repository_single_replication`, `artifactory_local_repository_multi_replication`, `artifactory_remote_repository_replication`, `artifactory_pull_replication` (deprecated), `artifactory_push_replication` (deprecated) | `PUT/GET/POST/DELETE /artifactory/api/replications/{repo}`; multi `/replications/multiple/{repo}`; each verifies `rclass` through `GET /artifactory/api/repositories/{key}` |
| Lifecycle | `artifactory_release_bundle_v2`, `artifactory_release_bundle_v2_promotion` | `POST /lifecycle/api/v2/release_bundle?async=false[&project=]` (+ `X-JFrog-Signing-Key-Name`), `GET/DELETE /lifecycle/api/v2/release_bundle/records/{name}/{version}`; `POST /lifecycle/api/v2/promotion/records/{name}/{version}`, `GET/DELETE .../{created}`; 501 |
| Webhooks (24) | `artifactory_{artifact, artifact_property, artifact_lifecycle, docker, build, release_bundle, distribution, artifactory_release_bundle, destination, user, release_bundle_v2, release_bundle_v2_promotion}_webhook` and the matching `_custom_webhook` | `POST /event/api/v1/subscriptions` **201**, `GET .../{key}` 200, `PUT` **204**, `DELETE` 204. Event names are validated client-side and echoed verbatim; see [07-webhooks-retention-projects.md](07-webhooks-retention-projects.md) |

Data sources: `artifactory_file`, `artifactory_fileinfo`, `artifactory_file_list`,
`artifactory_group`, `artifactory_user`, `artifactory_permission_target`,
`artifactory_repositories` (`GET /artifactory/api/repositories/?type=&packageType=&project=`; observed against artifactweave on 2026-10-12: the provider sends a **trailing slash** on this path, so the router must treat `/api/repositories/` and `/api/repositories` alike),
`artifactory_local` (deprecated), and one data source per repository type
(`artifactory_local_*`, `artifactory_remote_*`, `artifactory_virtual_*`,
`artifactory_federated_*`).

## `jfrog/platform`

| Resource | Endpoints and required status | Gate |
|---|---|---|
| `platform_permission` | `POST /access/api/v2/permissions` 201, `GET/DELETE .../{name}` 200/204, `PUT/DELETE .../{name}/{artifact\|build\|release_bundle\|destination\|pipeline_source}` | 7.72.0 |
| `platform_group`, `platform_group_members` | `POST /access/api/v2/groups` **201**, `GET/PATCH .../{name}` 200, `DELETE` **204**, `PATCH .../{name}/members` 200 | `roles` 7.128.0; no other gate |
| `platform_global_role` | `POST /access/api/v1/roles` 201 (`{code,message}`), `GET/PUT/DELETE .../{name}` 200 | |
| `platform_oidc_configuration` | `POST /access/api/v1/oidc` **200**, `GET/PUT/DELETE .../{name}` 200; sends `organization` (required for GitHub types) and `project_key`; `provider_type` in `generic`, `GitHub`, `GitHub Enterprise`, `Azure` | 7.73.1; Access 7.138 / 7.144 |
| `platform_oidc_identity_mapping` | `POST /access/api/v1/oidc/{provider}/identity_mappings` 200, `GET/PUT/DELETE .../{name}` 200 | |
| `platform_saml_settings` | `POST /access/api/v1/saml`, `GET/PUT/DELETE .../{name}`; 501 in v1 | 7.83.1 |
| `platform_scim_user`, `platform_scim_group` | `/access/api/v1/scim/v2/Users\|Groups[/{id}]`; 501 in v1 | |
| `platform_crowd_settings`, `platform_http_sso_settings` | `PUT/GET /access/api/v1/crowd`, `/access/api/v1/httpsso`; 501 in v1 | 7.64.0 / 7.57.1 |
| `platform_aws_iam_role` | `PUT /access/api/v1/aws/iam_role`, `GET/DELETE .../{username}`; 501 | 7.90.10 |
| `platform_license` | `POST /artifactory/api/system/licenses`, `GET /artifactory/api/system/license` | |
| `platform_reverse_proxy` | `GET/POST /artifactory/api/system/configuration/webServer` 200; reads and writes `httpsPort` (docs say `sslPort`: both are emitted), never sends `serverNameExpression` or `artifactoryPort`, allows `dockerReverseProxyMethod: DIRECT` | |
| `platform_workers_service` | `POST/PUT /worker/api/v1/workers`, `GET/DELETE .../{key}`; ignored (501) | 7.94 |
| `platform_lifecycle_stage`, `platform_lifecycle` | `POST /access/api/v2/stages`, `GET/PATCH/DELETE .../{name}[?project_key]`; `GET/PATCH /access/api/v2/lifecycle`; 501 | Access 7.155.0 |
| `platform_myjfrog_ip_allowlist` | deprecated stub; moved to the `jfrog/myjfrog` provider (SaaS only) | |

No active data sources.

## `jfrog/project`

| Resource | Endpoints and required status |
|---|---|
| `project` | `POST /access/api/v1/projects` 201, `GET/PUT/DELETE .../{key}` 200/200/204; sends `admin_privileges{manage_members,manage_resources,manage_remote_repository,index_resources}`, `soft_limit`, `storage_quota_email_notification`; nested users, groups, roles and repositories go through the endpoints below |
| `project_user`, `project_group` | `GET /access/api/v1/projects/{key}/users\|groups` 200, `PUT/DELETE .../{users\|groups}/{name}` 200/204 |
| `project_role` | `GET/POST /access/api/v1/projects/{key}/roles` 200/201, `PUT/DELETE .../roles/{name}` 200/204; validator has 32 actions and omits `READ_POLICIES_SECURITY` |
| `project_repository` | `PUT /access/api/v1/projects/_/attach/repositories/{repo}/{key}?force=true` **204**, then polls `GET /artifactory/api/repositories/{key}` with backoff until `projectKey` equals the project (attach must be synchronous); `DELETE .../attach/repositories/{repo}` 204; `GET /artifactory/api/repositories?project={key}`; `GET /access/api/v1/projects/_/repositories/{repo}` |
| `project_share_repository`, `project_share_repository_with_all` | `PUT/DELETE /access/api/v1/projects/_/share/repositories/{repo}/{target_project}` 204 (≥7.90.1); `.../share/repositories/{repo}` for all |
| `project_environment` | `POST/GET /access/api/v1/projects/{key}/environments` 201/200, `POST .../{env}/rename` 200, `DELETE .../{env}` 204 |

No data sources.

## CI notes

`terraform init` only downloads providers from registry.terraform.io and never contacts
the server; provider configuration runs at `plan`. The P0 exit criterion is therefore a
`terraform plan` with a configured `provider "artifactory"` block and a
`data "artifactory_repositories"` block returning no errors. The CI fixture in
`test/terraform/` uses a pre-populated provider mirror (`terraform providers mirror`,
cached between runs) so the acceptance job needs no outbound access to the registry and
the runner hardening policy can later be set to block egress.

Primary URLs: https://registry.terraform.io/providers/jfrog/artifactory/latest/docs,
https://registry.terraform.io/providers/jfrog/platform/latest/docs,
https://registry.terraform.io/providers/jfrog/project/latest/docs,
https://github.com/jfrog/terraform-provider-artifactory/tree/master/docs/resources,
https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/provider/framework.go,
https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/provider/resources.go,
https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/resource/repository/repository.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/util.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/util/provider.go,
https://github.com/jfrog/terraform-provider-shared/blob/main/client/client.go,
https://github.com/jfrog/terraform-provider-platform/tree/main/pkg/platform,
https://github.com/jfrog/terraform-provider-project/tree/master/pkg/project/resource.
