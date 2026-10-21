# 07: Webhooks, retention policies and projects

Checked against: Artifactory 7.161.32

Three platform features that GitOps users manage through Terraform and that artifactweave
plans to implement in P11: event subscriptions (`/event/api/v1`), package cleanup policies
(`/artifactory/api/cleanup`) and projects (`/access/api/v1/projects`). Each section records
the exact names, shapes and status codes the `jfrog/artifactory` and `jfrog/project`
providers depend on.

P11 has not started at the P8 closeout. The schemas and storage contracts were built
earlier; they do not make event delivery, cleanup-policy execution or project
management available. Global environments and repository `projectKey` persistence
already exist, but are not the full project lifecycle described below. See the
[build status](../implementation/plan.md#build-status).

Research baseline: **2026-10-12**. Sources: the per-endpoint pages under
https://docs.jfrog.com/integrations/reference/jfrog-platform-webhooks-api/llms.txt,
https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt and
https://docs.jfrog.com/projects/reference/llms.txt, plus the provider sources. Items marked
*(unverified)* lack a primary source.

## Webhooks (`/event/api/v1`)

Auth: platform admin, project admin (limited to the domains `artifact`, `artifact_property`,
`docker`, `build`), the Manage Webhooks role (7.137+), or a token scoped
`system:webhooks@<project_key>/<jfrog_url>:r|w|d|x` (7.135+).

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/event/api/v1/subscriptions` | 200 | array of subscriptions, secrets redacted. From SaaS 7.172.1 ([release notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71721-saas)) both GET endpoints return an empty `secret` and a boolean `secret_configured`; not yet in self-managed 7.161.32 |
| POST | `/event/api/v1/subscriptions` | **201** | |
| GET | `/event/api/v1/subscriptions/{key}` | 200 | |
| PUT | `/event/api/v1/subscriptions/{key}` | **204** | `project_key` is immutable; custom-webhook secrets: omit keeps, a value rotates, `""` wipes, an absent name removes |
| DELETE | `/event/api/v1/subscriptions/{key}` | 204 | the [reference page](https://docs.jfrog.com/integrations/reference/deletewebhooksubscription) documents 204, 401, 403 and 404 |
| POST | `/event/api/v1/subscriptions/test` | 200 | body is a create body; scope `:x` |
| GET | `/event/api/v1/troubleshooting?subscription&target&start&end&count` | 200 | streams when only `start` is given |

Subscription shape:

```json
{"key":"npm-deploys","description":"...","enabled":true,"project_key":"myproj","debug":false,
 "event_filter":{"domain":"artifact","event_types":["deployed","deleted"],
   "criteria":{"anyLocal":true,"anyRemote":false,"anyFederated":false,
               "repoKeys":["npm-local"],"includePatterns":["**"],"excludePatterns":[]}},
 "handlers":[{"handler_type":"webhook","url":"https://ci.example.com/hook",
              "secret":"...","use_secret_for_signing":true,"proxy":"corp-proxy",
              "custom_http_headers":[{"name":"X-Env","value":"prod"}]}]}
```

`key` matches `^[A-Za-z][A-Za-z0-9_\-]+$` and is at most 500 characters. `handlers` has
exactly one element.

Handler types:

| `handler_type` | Fields |
|---|---|
| `webhook` | `url, proxy, secret, use_secret_for_signing, custom_http_headers[{name,value}]` |
| `custom-webhook` | `url, proxy, method (POST\|PUT\|GET\|PATCH\|DELETE), payload, http_headers[{name,value}], secrets[{name,value}]`; `payload` is a Go template with `{{.data.x}}`, `{{json .data}}`, `{{.secrets.name}}`, `{{.userContext.id}}` |

Domains and event types. The Terraform provider validates names client-side with `OneOf`
and the server must echo them verbatim
(https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/resource/webhook/resource_artifactory_webhook.go,
https://docs.jfrog.com/integrations/docs/webhook-event-types):

| Domain | Event types | Criteria |
|---|---|---|
| `artifact` | `deployed`, `deleted`, `moved`, `copied`, `cached` | `anyLocal, anyRemote, anyFederated, repoKeys[], includePatterns[], excludePatterns[]` |
| `artifact_property` | `added`, `deleted` | as `artifact` |
| `docker` | `pushed`, `deleted`, `promoted` | as `artifact` |
| `build` | `uploaded`, `deleted`, `promoted` | `anyBuild, selectedBuilds[], includePatterns[], excludePatterns[]`; project-scoped from 7.122.2 |
| `user` | `locked` | none |
| `release_bundle` | `created`, `signed`, `deleted` | `anyReleaseBundle, registeredReleaseBundlesNames[]` |
| `distribution` | `distribute_started`, `distribute_completed`, `distribute_aborted`, `distribute_failed`, `delete_started`, `delete_completed`, `delete_failed` | as `release_bundle` |
| `artifactory_release_bundle`, `destination` | `received`, `delete_started`, `delete_completed`, `delete_failed` | as `release_bundle` (7.15.1+) |
| `release_bundle_v2` | `release_bundle_v2_started`, `release_bundle_v2_failed`, `release_bundle_v2_completed` | `anyReleaseBundle, selectedReleaseBundles` (map of name to versions); project from 7.125.3 |
| `release_bundle_v2_promotion` | `release_bundle_v2_promotion_started`, `release_bundle_v2_promotion_failed`, `release_bundle_v2_promotion_completed` | `selectedEnvironments[]` |
| `artifact_lifecycle` | `archive`, `restore` | as `artifact` |
| `xray_scan_status` | `done`, `failed`, `partial`, `not_supported` | Xray 3.150+; P26 producer, not emitted today |
| `app_trust` | gate, application, version and release lifecycle events | `projectKey, applicationKeys[], stages[]`; P25 producer, not emitted today |
| `curation` | block, waiver and policy events | none; P27 producer, not emitted today |

The planned P11 delivery engine emits `artifact`, `artifact_property`, `docker`,
`build` and `user` events. All Event routes currently inherit generated 501 handlers;
subscriptions do not yet apply or deliver. Release/Distribution producers belong to
P15, archive/restore to P16, RBv2 to P24, AppTrust to P25, Xray to P26 and Curation to
P27. P20 maps every accepted domain to its producer and executable delivery test;
accepting a domain that never fires is not its completion criterion. Additional
estate/pipeline integration events belong to P40 under the
[capability roadmap](../implementation/capability-roadmap.md).

Signing: the `X-JFrog-Event-Auth` request header carries the raw secret when
`use_secret_for_signing` is false and the hex HMAC-SHA256 of the body when it is true. This
comes from JFrog's guide text; no reference page in the index documents the header
*(unverified)*, so a real delivery is captured before the format is fixed
(see [12-open-questions.md](12-open-questions.md)). The current webhook pages still do not
document it: **[I]**, unconfirmed for 7.x. From 7.161.15, artifact event payloads carry a
`user_context` naming the user or token that caused the event ([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)); the
[event types](https://docs.jfrog.com/integrations/docs/webhook-event-types) page does not
show its shape yet (**[I]**).

## Package cleanup policies (`/artifactory/api/cleanup`, Artifactory ≥7.98.2)

Auth: Basic, API key or Bearer; platform admin, or project admin with `projectKey`.

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/artifactory/api/cleanup/packages/policies[?projectKey=]` | 200 | |
| POST | `/artifactory/api/cleanup/packages/policies/{policyKey}` | **201** | `enabled` must be false (or absent) on create; sending true fails |
| GET | `/artifactory/api/cleanup/packages/policies/{policyKey}[?checkRepoAvailability=]` | 200 | |
| PUT | `/artifactory/api/cleanup/packages/policies/{policyKey}` | 200 | |
| DELETE | `/artifactory/api/cleanup/packages/policies/{policyKey}` | **200** | the provider requires 200 |
| POST | `/artifactory/api/cleanup/packages/policies/{policyKey}/enablement` | 200 | `{"enabled":true}`; the provider sends `enabled` with `omitempty` on create and calls this only when true; update is PUT then enablement |
| PUT | `/artifactory/api/cleanup/packages/policies/{policyKey}/run?dryRun=` | 202 | `{"triggerId":"..."}` |
| PUT | `/artifactory/api/cleanup/policies/runs/{runId}` | 202 | `{"action":"stop"}` |
| PUT | `/artifactory/api/cleanup/policies/runs/forceStopAll` | 200 | |
| PUT | `/artifactory/api/cleanup/policies/forceDisableAll[?projectKey]` | 200 | |
| GET | `/artifactory/api/cleanup/policies/view?itemTypes=package,build,releaseBundle&policyKey&projectKey` | 200 | |
| GET | `/artifactory/api/cleanup/policies/runs/view?itemTypes&from&to&policyKey&projectKey&page&pageSize&sortBy&sortOrder&status&excludeBlankExecutions&projectKeyList` | 200 | |
| GET | `/artifactory/api/cleanup/policies/runs/{policyKey}/{triggerId}` | 200 | |
| GET | `/artifactory/api/cleanup/policies/runs/downloadReport?policyKey&runId` | 200 | `application/octet-stream` CSV |
| GET | `/artifactory/api/cleanup/policies/runs/summaryReports?policyKey&triggerId` | 200 | |

The same shape exists for `/artifactory/api/cleanup/builds/policies` (7.125+, `itemType`
`build`) and `/artifactory/api/cleanup/bundles/policies` (7.104.2+, `itemType`
`releaseBundle`); both are stubbed in v1.

Policy shape:

```json
{"key":"cleanup-old-npm","description":"at most 256 chars","cronExp":"0 0 2 ? * MON-SAT *",
 "projectKey":"myproj","itemType":"package","durationInMinutes":60,"enabled":false,"skipTrashcan":false,
 "searchCriteria":{"packageTypes":["npm","docker"],"repos":["**"],"excludedRepos":["npm-keep"],
   "includedPackages":["**"],"excludedPackages":["@acme/core"],
   "includeAllProjects":false,"includedProjects":["default","myproj"],
   "includeAllStages":true,"includedStages":[],
   "createdBeforeInDays":90,"lastDownloadedBeforeInDays":60,"keepLastNVersions":3,
   "includedProperties":{"cleanup":["true"]},"excludedProperties":{"pinned":["true"]},
   "includedPackageVersions":["1.*"],"excludedPackageVersions":["*.0"]}}
```

Required: `key`, `searchCriteria.packageTypes`, `searchCriteria.repos`,
`searchCriteria.includedPackages`. The `*InMonths` fields are pre-7.111 legacy. Package
types accepted by the provider: alpine, ansible, cargo, chef, cocoapods, composer, conan,
conda, debian, docker, gems, generic, go, gradle, helm, helmoci, huggingfaceml,
machinelearning, maven, npm, nuget, oci, puppet, pypi, sbt, swift, terraform,
terraformbackend, yum. `itemType` is `package | build | releaseBundle`.

Later additions in the release notes: version patterns (7.125.7, SaaS 7.127.0), lifecycle
stages and optional path patterns (SaaS 7.147.1, self-managed 7.161.15), and time, property
and version conditions combined in one policy (SaaS 7.161.5, self-managed 7.161.15)
([7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)). The published create-policy definition has no path-pattern field
yet, so its name is **[I]**.

Run status enum: `IN_PROGRESS`, `IN_PROGRESS_WITH_ERRORS`, `COMPLETED`,
`COMPLETED_WITH_ERRORS`, `STOPPED`, `STOPPING`, `FAILED`, `PENDING_WARM`, `PARTIAL_WARMED`,
`WARMED`.

`cronExp` uses Quartz syntax (six or seven fields with seconds, `?`, `L`, `W`, `#` and an
optional year); see [ADR 0010](decisions/0010-retention-trash-and-gc.md).
`keepLastNVersions`, `includedPackageVersions` and `excludedPackageVersions` are evaluated
over the package index ([ADR 0018](decisions/0018-package-index.md)) with the per-type
version comparator.

Cold-storage archive policies (`/artifactory/api/retention/archive/*`, Enterprise+) and
smart archiving (`/artifactory/api/archive/v2/*`, Cloud only, 7.111+) answer 501. Release
bundle v1 retention (`/artifactory/api/v1/bundle/retention/*`, Distribution) is ignored.

## Projects (`/access/api/v1/projects`)

Auth: Bearer; platform admin unless noted. Source:
https://docs.jfrog.com/projects/reference/llms.txt and
https://docs.jfrog.com/projects/docs/create-a-project.

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v1/projects` | 200 | platform admin sees all; project admins see their own (7.117.5+) |
| POST | `/access/api/v1/projects` | **201** | 409 on conflict |
| GET | `/access/api/v1/projects/{projectKey}` | 200 | platform or project admin |
| PUT | `/access/api/v1/projects/{projectKey}` | 200 | `project_key` is immutable and absent from the body |
| DELETE | `/access/api/v1/projects/{projectKey}` | 204 | |
| GET | `/access/api/v2/projects?role_name=&limit&descending_order&cursor` | 200 | projects for a global role: `{"projects":[{name,projectKey}],"cursor":"..."}` |
| GET | `/access/api/v1/projects/{k}/users` | 200 | or scope `project:<k>/members/users:r`; `{"members":[{name,roles[]}]}` |
| GET / PUT / DELETE | `/access/api/v1/projects/{k}/users/{user}` | 200 / 204 | PUT `{"roles":[...]}` |
| GET | `/access/api/v1/projects/{k}/groups` | 200 | scope `project:<k>/members/groups:r` |
| GET / PUT / DELETE | `/access/api/v1/projects/{k}/groups/{group}` | 200 / 204 | PUT `{"roles":[...]}` |
| GET / POST | `/access/api/v1/projects/{k}/roles` | 200 / 201 | scope `project:<k>/roles:r`; POST `{name,type,description,environments[],actions[]}` |
| GET / PUT / DELETE | `/access/api/v1/projects/{k}/roles/{role}` | 200 / 204 | PUT requires `type: "CUSTOM"` |
| PUT | `/access/api/v1/projects/_/attach/repositories/{repo}/{targetProjectKey}?force=` | **204** | the `project_repository` resource then polls `GET /artifactory/api/repositories/{repo}` until `projectKey` equals the project, so the attach must be reflected in repository configuration synchronously |
| DELETE | `/access/api/v1/projects/_/attach/repositories/{repo}` | 204 | unassign |
| GET | `/access/api/v1/projects/_/repositories/{repo}` | 200 | `{resource_name, assigned_to, environments[], shared_with_projects[], shared_with_all_projects, shared_read_only}` |
| PUT / DELETE | `/access/api/v1/projects/_/share/repositories/{repo}` | 204 | share or unshare with all projects |
| PUT / DELETE | `/access/api/v1/projects/_/share/repositories/{repo}/{targetProjectKey}?readOnly=` | 204 | share with one project (7.90.1+) |
| POST / GET | `/access/api/v1/projects/{k}/environments` | 201 / 200 | rename and delete as for global environments |

Project shape (create):

```json
{"project_key":"myproj","display_name":"My Project","description":"...",
 "storage_quota_bytes":10737418240,"soft_limit":false,"storage_quota_email_notification":true,
 "admin_privileges":{"manage_members":true,"manage_resources":true,"index_resources":true,
   "manage_remote_repository":true,"create_remote_repository":true,"delete_remote_repository":true,
   "create_local_repository":true,"delete_local_repository":true,
   "create_virtual_repository":true,"delete_virtual_repository":true}}
```

The `jfrog/project` provider sends only `manage_members`, `manage_resources`,
`manage_remote_repository` and `index_resources` plus `soft_limit` and
`storage_quota_email_notification`; artifactweave accepts and returns all of them. The
granular repository privileges exist from 7.146; when any is set, the legacy
`manage_resources` and `manage_remote_repository` mappings are ignored.

Semantics:

- `project_key` matches `^[a-z][a-z0-9-]{1,31}$` (2 to 32 characters, lower-case
  alphanumeric and hyphen, starting with a letter) and is immutable.
- Repositories created inside a project carry the `<project_key>-` prefix on their key
  (the docs example is `us-dev-docker_remote`), and the project's build-info repository is
  `<project_key>-build-info`.
- A repository created through the Artifactory API with `projectKey` must also set
  `environments`; each repository belongs to exactly one environment, `DEV` by default.
- Storage quota: warning at 75 percent, block at 100 percent on self-hosted; Terraform
  exposes `max_storage_in_gibibytes` (-1 unlimited), `block_deployments_on_limit`,
  `email_notification`.
- Predefined roles: Project Admin, Developer, Contributor, Viewer, Release Manager, Security
  Manager, Application Admin. The reference pages do not enumerate them; the names come
  from the UI docs and the provider examples *(unverified as an exhaustive list)*.

Role action vocabularies (from the provider validators):

- Global roles (25): `READ_REPOSITORY`, `ANNOTATE_REPOSITORY`, `DEPLOY_CACHE_REPOSITORY`,
  `DELETE_OVERWRITE_REPOSITORY`, `MANAGE_XRAY_MD_REPOSITORY`, `READ_RELEASE_BUNDLE`,
  `ANNOTATE_RELEASE_BUNDLE`, `CREATE_RELEASE_BUNDLE`, `DISTRIBUTE_RELEASE_BUNDLE`,
  `DELETE_RELEASE_BUNDLE`, `MANAGE_XRAY_MD_RELEASE_BUNDLE`, `READ_BUILD`, `ANNOTATE_BUILD`,
  `DEPLOY_BUILD`, `DELETE_BUILD`, `MANAGE_XRAY_MD_BUILD`, `READ_SOURCES_PIPELINE`,
  `TRIGGER_PIPELINE`, `READ_INTEGRATIONS_PIPELINE`, `READ_POOLS_PIPELINE`,
  `REPORTS_SECURITY`, `WATCHES_SECURITY`, `POLICIES_SECURITY`, `RULES_SECURITY`,
  `READ_POLICIES_SECURITY`.
- Project roles (32): the 25 above without `READ_POLICIES_SECURITY`, plus
  `MANAGE_INTEGRATIONS_PIPELINE`, `MANAGE_SOURCES_PIPELINE`, `MANAGE_POOLS_PIPELINE`,
  `TRIGGER_SECURITY`, `ISSUES_SECURITY`, `LICENCES_SECURITY`, `MANAGE_MEMBERS`,
  `MANAGE_RESOURCES`.
- `type` is `PREDEFINED | CUSTOM | CUSTOM_GLOBAL`; global roles are created with
  `CUSTOM_GLOBAL` and the create response is `{code,message}`.

Primary URLs: https://docs.jfrog.com/integrations/reference/jfrog-platform-webhooks-api/llms.txt,
https://docs.jfrog.com/integrations/docs/webhook-event-types,
https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt,
https://docs.jfrog.com/projects/reference/llms.txt,
https://docs.jfrog.com/projects/docs/create-a-project,
https://github.com/jfrog/terraform-provider-artifactory/tree/master/pkg/artifactory/resource/webhook,
https://github.com/jfrog/terraform-provider-artifactory/blob/master/pkg/artifactory/resource/configuration/resource_artifactory_package_cleanup_policy.go,
https://github.com/jfrog/terraform-provider-project/tree/master/pkg/project/resource.
