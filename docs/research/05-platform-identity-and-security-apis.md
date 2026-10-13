# 05: Platform identity and security APIs

The identity surface artifactweave must mirror: the legacy Artifactory security API that
the JFrog CLI and the `jfrog/artifactory` Terraform provider still call, the Access v2
identity APIs that the `jfrog/platform` provider and newer resources use, access tokens
and their scope grammar, and the authentication mechanisms every endpoint accepts. Each
table carries the status codes the clients hard-check; those codes are the oracle for the
table-driven status test described in [ADR 0002](decisions/0002-spec-first-flat-crud-handwritten-item-paths.md).

Research baseline: **2026-10-12**. Sources are the per-endpoint pages on docs.jfrog.com
(each embeds an OpenAPI 3.1 definition), the Terraform provider sources at the versions in
[08-terraform-providers.md](08-terraform-providers.md) and `jfrog-client-go` v1.55.0.
Claims not confirmed against a primary source are marked *(unverified)*.

## Error envelopes

| Service | Envelope |
|---|---|
| Artifactory (`/artifactory/...`) | `{"errors":[{"status":<int>,"message":"<string>"}]}` |
| Access and Projects (`/access/...`) | `{"errors":[{"code":"<string>","message":"<string>"}]}` |

## Legacy Artifactory security API (`/artifactory/api/security/...`)

Every page in this family carries the note that the security APIs are moving to Access
(since 7.49.3) and will be deprecated. They are still called by `artifactory_user`,
`artifactory_group`, `artifactory_permission_target` and the JFrog CLI, so artifactweave
serves thin shims over the same internal model as Access v2.

Source index: https://docs.jfrog.com/administration/reference/jfrog-artifactory-security-api/llms.txt
and https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis.

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/api/security/users` | 200 | `[{name,uri,realm}]`; deprecated in favour of Access v2 |
| GET | `/api/security/users/{userName}` | 200 | legacy user shape below |
| PUT | `/api/security/users/{userName}` | 201 | create or replace; requires `name`, `email`, `password` |
| POST | `/api/security/users/{userName}` | 200 | update |
| DELETE | `/api/security/users/{userName}` | 200 | |
| GET | `/api/security/groups` | 200 | |
| GET | `/api/security/groups/{groupName}?includeUsers=true\|false` | 200 | the provider always sends `includeUsers` |
| PUT | `/api/security/groups/{groupName}` | **201** | `artifactory_group` requires 201 on create and on update-with-members; the CLI accepts 200 or 201 |
| POST | `/api/security/groups/{groupName}` | 2xx | update |
| DELETE | `/api/security/groups/{groupName}` | 200 | |
| GET | `/api/security/permissions` | 200 | v1 targets, `[{name,uri}]`, repositories only |
| GET | `/api/security/permissions/{name}` | 200 | |
| PUT | `/api/security/permissions/{name}` | 200 | create or replace, v1 shape |
| DELETE | `/api/security/permissions/{name}` | 200 | |
| GET | `/api/v2/security/permissions` | 200 | admin or Manage on the target |
| GET | `/api/v2/security/permissions/{name}` | 200 | |
| HEAD | `/api/v2/security/permissions/{name}` | 200 / 404 | existence check |
| POST | `/api/v2/security/permissions/{name}` | **201** | create only; 409 when it exists (jfrog-client-go treats 409 as "exists") |
| PUT | `/api/v2/security/permissions/{name}` | **200** | create or replace; an omitted section (`build`, `releaseBundle`) is deleted |
| DELETE | `/api/v2/security/permissions/{name}` | 200 | |
| GET | `/api/v2/security/permissions/users/{u}`, `/groups/{g}` | 200 | targets per principal |
| POST | `/api/security/apiKey` | **201** | jfrog-client-go hard-checks 201 although the docs say 200. artifactweave answers 400 "API key creation is disabled", matching Artifactory ≥7.98 defaults |
| PUT / GET / DELETE | `/api/security/apiKey[?deleteAll=1]`, `DELETE /api/security/apiKey/{username}` | 200 / 404 | GET returns 404 when no key exists |
| GET | `/api/security/encryptedPassword` | 200 | `text/plain`; caller must authenticate with the clear-text password |
| GET / PUT | `/api/security/userLockPolicy` | 200 | `{"enabled":bool,"loginAttempts":int}` |
| GET | `/api/security/lockedUsers` | 200 | `["usera","userb"]` |
| POST | `/api/security/unlockUsers` | 200 | body `["userA"]`; the CLI uses the deprecated path form `POST /api/security/unlockUsers/{name}` with no body, so both are served |
| POST | `/api/security/unlockAllUsers` | 200 | |
| POST | `/api/security/users/authorization/expirePassword` | 200 | body `["davids"]` |
| POST | `/api/security/users/authorization/unexpirePassword/{userName}` | 200 | |
| GET / PUT | `/api/security/configuration/passwordExpirationPolicy` | 200 | `{"enabled":true,"passwordMaxAge":60,"notifyByEmail":true}`. The docs show quoted strings but `artifactory_password_expiration_policy` decodes `bool` and `int64`, so real JSON types are emitted |
| POST | `/api/security/token` | 200 | legacy create or refresh, form-encoded: `grant_type` (`client_credentials` default, `refresh_token`), `username`, `scope` (legacy forms `member-of-groups:<g>`, `api:*`, `jfrt@<id>:admin`), `expires_in`, `refreshable`, `audience`, `refresh_token`, `access_token`. Not on JFrog's deprecation page; `jf` still implements it |
| GET | `/api/security/token` | 200 | `{"tokens":[{token_id,issuer,subject,expiry,refreshable,issued_at}]}` |
| POST | `/api/security/token/revoke` | 200 | form `token` or `token_id`; 200 even if already revoked |
| GET | `/api/system/security/certificates` | 200 | remote-repository client certificates: `[{certificateAlias,issuedTo,issuedBy,issuedOn,validUntil,fingerPrint}]` |
| POST | `/api/system/security/certificates/{alias}` | 200 | PEM body, `text/plain`; response `{status,message[]}` |
| DELETE | `/api/system/security/certificates/{alias}` | 200 | |
| PUT / GET | `/api/gpg/key/public`, `/api/gpg/key/private`, `/api/gpg/passphrase` | 200 | `text/plain`; passphrase via header `X-GPG-PASSPHRASE` |
| POST | `/api/security/keypair` | **201** | `{pairName,pairType:"RSA"\|"GPG",alias,privateKey,publicKey,passphrase,vaultKey,vaultPublicKey{path,key}}` |
| GET | `/api/security/keypair` | 200 | list |
| GET / PUT / DELETE | `/api/security/keypair/{name}` | 200 | GET returns only `{pairName,pairType,alias,publicKey}` |
| GET | `/api/security/keypair/public/repositories/{repo}` | 200 | `text/plain` public key assigned to a repository |
| POST | `/api/v2/repositories/{repo}/keyPairs`, `.../primary`, `.../secondary` | 200 | body is the key-pair name, `text/plain` |
| POST | `/api/security/keys/trusted` | **201** | distribution public keys; GET returns `{"keys":[{kid,alias,fingerprint,key,issued_on,issued_by,valid_until}]}`; DELETE `.../{key_id}` 204 or 200 |
| POST | `/api/system/encrypt`, `/api/system/decrypt` | 200 | `DONE` |
| GET | `/api/system/service_id` | 200 | `jfrt@<uuid>`, `text/plain` |

No SSH-key endpoints exist in current docs or clients. The old `/api/ssh/key` page is gone.

### Legacy JSON shapes

User (`GET /api/security/users/{u}`; PUT uses the same keys):

```json
{"name":"davids","email":"davids@example.com","password":"...","admin":false,"profileUpdatable":true,
 "disableUIAccess":false,"internalPasswordDisabled":false,"groups":["readers"],"realm":"internal",
 "lastLoggedIn":"2026-01-01T00:00:00.000Z","status":"ENABLED","mfaStatus":"NONE",
 "policyViewer":false,"policyManager":false,"watchManager":false,"reportsManager":false}
```

`status` is upper-case `ENABLED | DISABLED | LOCKED`; `mfaStatus` is `NONE | ENABLED | DISABLED`.

Group: `{name,description,autoJoin,adminPrivileges,realm,realmAttributes,userNames[],watchManager,policyManager,reportsManager}`.

Permission target v1 (`PUT /api/security/permissions/{name}`):

```json
{"name":"populateCaches","includesPattern":"**","excludesPattern":"",
 "repositories":["libs-release-local","ANY REMOTE"],
 "principals":{"users":{"bob":["r","w","m"]},"groups":{"readers":["r"]}}}
```

Codes: `m` manage, `d` delete, `w` deploy, `n` annotate, `r` read, `mxm` managedXrayMeta,
`x` distribute. Virtual repositories are not allowed as targets.

Permission target v2 (`/api/v2/security/permissions/{name}`, `name` at most 64 characters):

```json
{"name":"java-developers",
 "repo":{"include-patterns":["**"],"exclude-patterns":[""],
         "repositories":["ANY LOCAL","libs-release-local"],
         "actions":{"users":{"bob":["read","write","annotate","delete","manage","managedXrayMeta","distribute"]},
                    "groups":{"dev":["read"]}}},
 "build":{"include-patterns":[""],"exclude-patterns":[""],"repositories":["artifactory-build-info"],"actions":{}},
 "releaseBundle":{"include-patterns":[""],"exclude-patterns":[""],"repositories":["release-bundles"],"actions":{}}}
```

`build.repositories` is fixed to `artifactory-build-info`; `repositories` accepts `ANY`,
`ANY LOCAL`, `ANY REMOTE`; patterns are at most 1024 characters. Note the mixed kebab-case
and camelCase keys inside one object: this is why wire models live in packages that
`tagliatelle` ignores ([ADR 0002](decisions/0002-spec-first-flat-crud-handwritten-item-paths.md)).

## Access identity (`/access/api/...`)

Source index: https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt.
All endpoints take Bearer auth; unless noted they require a platform admin.

### Users v2

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v2/users` | 200 | admin or scope `system:identities:r`; query `limit` (default 1000), `cursor`, `descendingOrder`, `status` (`invited`, `enabled`, `disabled`, `locked`), `username` (contains), `onlyAdmins`, `role`, `resourceType`, `resourceName`, `projectKey`; response `{"users":[{username,uri,realm,status}],"cursor":"..."}` |
| POST | `/access/api/v2/users` | **201** | 409 when the user exists |
| GET | `/access/api/v2/users/{username}` | 200 | |
| PATCH | `/access/api/v2/users/{username}` | 200 | partial; omitted fields unchanged; `password` is mandatory when `internal_password_disabled` flips to false |
| DELETE | `/access/api/v2/users/{username}` | 204 | |
| PATCH | `/access/api/v2/users/{username}/groups` | 200 | `{"add":[...],"remove":[...]}` returns `{"groups":[...]}` |
| PUT | `/access/api/v2/users/{username}/password` | 204 | `{"password":"..."}`; admin or self |
| POST | `/access/api/v2/users/{username}/password/expire`, `/unlock` | 204 | |
| POST | `/access/api/v2/users/expire_password_for_all_users`, `/unexpire_password_for_all_users` | 200 | |

The `jfrog/artifactory` provider uses these on Artifactory ≥7.84.3 and the legacy
`/api/security/users` endpoints below that version.

Create body: `{username,password,email,groups[],admin,profile_updatable,internal_password_disabled,disable_ui_access,watch_manager,policy_viewer,policy_manager,reports_manager,resources_manager,manage_webhook,platform_auditor}`;
`username` and `email` are required, `password` is optional only when
`internal_password_disabled` is true. Details add `effective_admin`, `last_logged_in`,
`realm` (`internal`), `status` (lower-case `invited | enabled | disabled | locked`).

### Groups v2

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v2/groups` | 200 | query `limit`, `cursor`, `descendingOrder`, `groupName`, `role`, `resourceType`, `resourceName`, `projectKey`; response `{"groups":[{group_name,uri}],"cursor":"..."}` |
| POST | `/access/api/v2/groups` | **201** | `members` and `import_from_ldap_group_setting` are mutually exclusive; 409 on conflict; `platform_group` requires 201 |
| GET | `/access/api/v2/groups/{name}` | 200 | |
| PATCH | `/access/api/v2/groups/{name}` | 200 | `realm`, `realm_attributes` and `members` are not updatable here |
| DELETE | `/access/api/v2/groups/{name}` | **204** | `platform_group` requires 204 |
| PATCH | `/access/api/v2/groups/{name}/members` | 200 | `{"add":[...],"remove":[...]}` returns `{"members":[...]}` |

Create body: `{name,description,auto_join,admin_privileges,external_id,import_from_ldap_group_setting,members[],reports_manager,watch_manager,policy_manager,policy_viewer,manage_resources,manage_webhook}`;
details add `realm` and `realm_attributes`. The `roles` field needs Artifactory ≥7.128.0;
there is no other version gate on groups.

### Permissions v2 (Artifactory ≥7.72.0)

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v2/permissions` | 200 | admin or `system:permissions:r`; query `cursor`, `limit` (1 to 99999), `user` (csv), `group` (csv), `include_inherited_groups` (default true), `expand` (needs `user` or `group`) |
| POST | `/access/api/v2/permissions` | **201** | 409 on conflict |
| GET | `/access/api/v2/permissions/{name}` | 200 | |
| DELETE | `/access/api/v2/permissions/{name}` | 204 | |
| GET / PUT / PATCH / DELETE | `/access/api/v2/permissions/{name}/{resourceType}` | 200 / 204 | `resourceType` in `artifact`, `build`, `release_bundle` (the provider also uses `destination` and `pipeline_source`) |

```json
{"name":"my-permission","description":"...",
 "resources":{
   "artifact":{"actions":{"users":{"alice":["READ","WRITE"]},
                          "groups":{"devs":["READ","ANNOTATE","WRITE","DELETE","SCAN","MANAGE"]}},
               "targets":{"ANY LOCAL":{"include_patterns":["**"],"exclude_patterns":[]},
                          "my-docker-local":{"include_patterns":["**"],"exclude_patterns":["*.tmp"]}}},
   "build":{"actions":{"groups":{"devs":["READ"]}},
            "targets":{"artifactory-build-info":{"include_patterns":["**"],"exclude_patterns":["*.js"]}}},
   "release_bundle":{"actions":{"users":{"alice":["READ","ANNOTATE","WRITE","EXECUTE","DELETE","SCAN","MANAGE"]}},
                     "targets":{"release-bundles-v2":{"include_patterns":["**"],"exclude_patterns":[]}}}}}
```

The OpenAPI definition types only `artifact`, `build` and `release_bundle` and leaves
`actions` as free strings. The vocabulary `READ ANNOTATE WRITE DELETE SCAN MANAGE EXECUTE`,
the extra resource types `destination` and `pipeline_source`, and the special targets
`ANY LOCAL`, `ANY REMOTE`, `ANY DISTRIBUTION` come from the provider resource
(https://github.com/jfrog/terraform-provider-platform/blob/main/docs/resources/permission.md).
`build` requires exactly one target.

### Access tokens v1

Source: https://docs.jfrog.com/administration/docs/access-tokens and the per-endpoint pages.

| Method | Path | Status | Notes |
|---|---|---|---|
| POST | `/access/api/v1/tokens` | **200** | create or refresh; JSON or form-urlencoded; non-admins can only create identity tokens; `artifactory_scoped_token` requires exactly 200 |
| GET | `/access/api/v1/tokens` | 200 | admin sees all, a user sees their own; `{"tokens":[TokenDetails]}`; `last_used` needs `access_token_last_used_enabled` (7.108.3+) |
| GET | `/access/api/v1/tokens/{tokenId}` | 200 / 404 | `me` is the current token; the provider removes the resource from state on 404 |
| DELETE | `/access/api/v1/tokens/{tokenId}` | 200 | revoke by id; `me` allowed |
| DELETE | `/access/api/v1/tokens/revoke` | 200 | admin; body `{"token":"<jwt or reference>"}` returns `{"revoked_token_id":"..."}` |
| GET / PUT | `/access/api/v1/config/token/default_expiry` | 200 | `{"default_token_expiration":3600}` |
| POST | `/access/api/v1/service_trust/pairing/{useCase}` | 200 | `mission-control`, `bridge`, `platform-federation`; out of scope |

Create request:

```json
{"grant_type":"client_credentials","username":"ci-bot",
 "scope":"applied-permissions/groups:readers artifact:npm-local/**:r,w",
 "expires_in":3600,"refreshable":false,"description":"...","audience":"*@*",
 "include_reference_token":true,"force_revocable":false,"project_key":"myproj"}
```

The provider sends `project_key` even when empty (`""`) and never sends `force_revocable`.
For `grant_type: refresh_token` the body carries `refresh_token`.

Response: `{"token_id":"...","access_token":"...","refresh_token":"...","reference_token":"...","expires_in":3600,"scope":"...","token_type":"Bearer"}`.
The docs omit `token_id` from the response schema but the provider reads it to drive
`GET`/`DELETE /tokens/{id}`, so it is always returned.

`TokenDetails`: `{token_id, subject, expiry, issued_at, issuer, description, refreshable, scope, last_used}`.
There is no `audience` field. `subject` has the form `jfac@<service-id>/users/<name>`.

Defaults and limits: `expires_in` defaults to the value of `default_expiry` (3600 s);
`0` means never expires and is admin-only; tokens with an expiry below the
`revocable-expiry-threshold` (default 21600 s) are not revocable unless `force_revocable`;
refresh grace `token.refresh-expiry` 24 h; `token.max-expiry` 0 means unlimited; `scope`
at most 500 characters, `username` at most 255, `description` at most 1024.

### Scope grammar

| Scope | Meaning |
|---|---|
| `applied-permissions/user` | the subject's own permissions (identity token, the default) |
| `applied-permissions/admin` | platform admin |
| `applied-permissions/groups:"g1","g 2"` | the union of the named groups' permissions |
| `applied-permissions/roles:<project>:developer,"Project Admin"` | project roles |
| `artifact:<repo or *>[/<ant path>]:<actions>` | actions `r,w,d,a,x,s,m` or `*` |
| `repo:<key>:r` | read a repository's configuration |
| `project:<key>/members/users:r`, `project:<key>/members/groups:r`, `project:<key>/roles:r` | project reads |
| `system:metrics:r`, `system:livelogs:r`, `system:identities:r|w`, `system:permissions:r`, `system:info:r`, `system:info/licenses:r`, `system:info/storage:r` | system scopes |
| `system:webhooks@<project>/<jfrog url>:r,w,d,x` | webhook management (7.135+) |

Token scopes only narrow what the subject already has.

### Reference tokens

A reference token is an opaque alias for a JWT, requested with `include_reference_token`.
The documented pattern is `\bcmVmd[A-Za-z0-9]{59}\b`, exactly 64 characters
(https://docs.jfrog.com/user-management/docs/identity-tokens; the access-tokens page's
"128 characters" is contradicted by it). `cmVmd` is the base64 of `ref` plus two bits of
the next byte; the real prefix is `cmVmdGtuOjAx` (`reftkn:01`). Reference tokens are
accepted as Bearer, as the Basic password and in `X-JFrog-Art-Api`, and are not valid
across a Circle of Trust.

### JWT payload quirk

`jf c add` extracts the username by splitting the JWT on `.` and decoding the payload with
Go's `base64.RawStdEncoding` (standard alphabet, no padding), then reading `sub`
(https://github.com/jfrog/jfrog-client-go/blob/master/auth/authutils.go). The subject must
contain `/users/<name>` or start with `jfrt@`. A base64url payload containing `-` or `_`
(or trailing `=`) fails silently and the CLI prompts for a username. artifactweave pads the
payload JSON with whitespace until its encoding contains none of those characters.

### Roles, environments, OIDC, SCIM, external auth, config

| Method | Path | Status | Notes |
|---|---|---|---|
| POST | `/access/api/v1/roles` | **201** | body `{name,description,type:"CUSTOM_GLOBAL",environments[],actions[]}`; response `{code,message}`, not the role |
| GET | `/access/api/v1/roles` | 200 | `type` enum `ADMIN | PREDEFINED | CUSTOM_GLOBAL` |
| GET / PUT / DELETE | `/access/api/v1/roles/{role}` | 200 | |
| POST | `/access/api/v1/environments` | **201** | `{"name":"..."}`, at most 32 characters, case-insensitive; built-ins `DEV`, `PROD` |
| GET | `/access/api/v1/environments` | 200 | |
| POST | `/access/api/v1/environments/{name}/rename` | 200 | `{"new_name":"..."}` |
| DELETE | `/access/api/v1/environments/{name}` | 204 | 404 when absent |
| POST / GET | `/access/api/v1/oidc` | **200** | create returns 200, not 201; body `{name,issuer_url,audience,description,provider_type,token_issuer,enable_permissive_configuration,use_default_proxy,azure_app_id,organization,project_key}`; `provider_type` in `generic | GitHub | GitHub Enterprise | Azure`; `organization` is required for GitHub types |
| GET / PUT / DELETE | `/access/api/v1/oidc/{name}` | 200 | |
| POST / GET | `/access/api/v1/oidc/{provider}/identity_mappings[?project_key=]` | 200 | `{name,description,priority,claims{...},token_spec{username,scope,audience,expires_in,username_pattern,groups_pattern,self_revocable}}` |
| PUT / DELETE | `/access/api/v1/oidc/{provider}/identity_mappings/{mapping}` | 200 | |
| POST | `/access/api/v1/oidc/token` | 200 | **no auth**; `{grant_type:"urn:ietf:params:oauth:grant-type:token-exchange",subject_token_type:"urn:ietf:params:oauth:token-type:id_token",subject_token,provider_name,project_key,identity_mapping_name,include_reference_token,application_key,job_id,run_id,repo,revision,branch}` returns `{access_token,token_type,expires_in,issued_token_type,username,reference_token}` |
| GET | `/access/api/v1/scim/v2/ResourceTypes`, `/Schemas[/{id}]`, `/ServiceProviderConfig` | 200 | `application/scim+json` |
| GET / POST | `/access/api/v1/scim/v2/Users`, `/Groups` | 200 / 201 | read needs admin or `system:identities:r`, write `system:identities:w`; 501 in artifactweave v1 ([ADR 0019](decisions/0019-human-authentication-tiers.md)) |
| GET / PUT / PATCH / DELETE | `/access/api/v1/scim/v2/Users/{id}`, `/Groups/{id}` | 200 / 204 | 501 in v1 |
| GET / POST | `/access/api/v1/saml` | 200 | plus `/saml/{name}` and `/saml/general_settings` `{enable,certificateIsPresent}`; 501 in v1 |
| GET / POST | `/access/api/v1/ldap/settings` | 200 | plus `/settings/{key}`, `/settings/test`; `/ldap/groups[/{name}]`, `.../refresh`; implemented in a later phase |
| GET / PUT | `/access/api/v1/httpsso`, `/access/api/v1/crowd` | 200 | 501 in v1 |
| GET / PUT | `/access/api/v1/config/security/authentication/basic_authentication_enabled` | 200 | `{"enabled":true}` |
| PUT / PATCH | `/access/api/v1/config/security/password_policy` | **204** | `{uppercase,lowercase,digit,special_char,length,not_match_old}`; PUT overwrites all, PATCH is partial; no body |
| GET | `/access/api/v1/cert/root`, `/access/api/v1/cert/ca` | 200 | `text/plain` |
| GET / PUT / DELETE | `/access/api/v1/vault/configs[/{key}]` | 200 | out of scope |
| PUT / GET / DELETE | `/access/api/v1/aws/iam_role[/{username}]` | 200 | `{username,iam_role}`; out of scope |
| GET / POST | `/access/api/v1/system/federation` | 200 | Access Federation, Enterprise+; ignored |
| POST | `/access/api/v1/system/backup/export`, `/import` | 202 | self-hosted; out of scope |

The global-role action vocabulary (25 values) and the project-role vocabulary (32 values,
which omits `READ_POLICIES_SECURITY`) are listed in
[07-webhooks-retention-projects.md](07-webhooks-retention-projects.md).

## Authentication accepted on every endpoint

Sources: https://docs.jfrog.com/user-management/docs/api-key,
https://docs.jfrog.com/administration/docs/access-tokens,
https://docs.jfrog.com/user-management/docs/identity-tokens.

1. **HTTP Basic** with `user:password` (internal users, not when MFA is enabled),
   `user:<API key>` (legacy), `user:<access token>` or `user:<reference token>`. The
   username must match the token's subject.
2. **Bearer** `Authorization: Bearer <JWT or reference token>`. All tokens are scoped since
   7.21.1; identity tokens carry `applied-permissions/user`.
3. **`X-JFrog-Art-Api: <API key or reference token>`**. API keys: deprecation announced in
   7.47.10, creation disabled by default from 7.98, end of life at the end of 2024.
   artifactweave accepts reference tokens in this header and refuses to create API keys.
4. **Anonymous** for ping, version, readiness, liveness, router health and the OIDC token
   exchange. Everything else needs `security.anonAccessEnabled` and a permission that grants
   the `anonymous` user.
5. Basic auth can be disabled platform-wide through
   `/access/api/v1/config/security/authentication/basic_authentication_enabled`.
6. The Docker token flow is described in [04-package-protocols.md](04-package-protocols.md);
   `/artifactory/api/docker/<repo>/v1|v2/auth` returns `{"auth":"<base64 user:pass>","email":"..."}`.

Built-in principals that Artifactory ships and artifactweave seeds at first start:
`admin`, `anonymous`, the `readers` group, and the `Anything` and `Any Remote` permission
targets. `anonAccessEnabled` toggles read access for `anonymous` through those targets.

## Tier notes

Shim only: `/api/security/users|groups|permissions` v1, `/api/security/token*`, API keys,
`encryptedPassword`. Enterprise+ only and ignored: Access Federation, Mission Control
(`/mc/api/v1/**`), Grid (`/grid/api/v1/**`), release-bundle permissions. SaaS only and
ignored: log shipping, MyJFrog, curation. 501 in v1: SAML, SCIM, Crowd, HTTP-SSO.

Primary URLs: https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt,
https://docs.jfrog.com/administration/reference/jfrog-artifactory-security-api/llms.txt,
https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis,
https://docs.jfrog.com/administration/docs/access-tokens,
https://docs.jfrog.com/user-management/docs/api-key,
https://docs.jfrog.com/user-management/docs/identity-tokens,
https://github.com/jfrog/terraform-provider-platform/blob/main/docs/resources/permission.md,
https://github.com/jfrog/jfrog-client-go/blob/master/auth/authutils.go.
