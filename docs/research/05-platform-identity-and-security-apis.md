# 05: Platform identity and security APIs

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))

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

P4 and the OIDC follow-up implement the local identity, token and permission flows
described here. LDAP and the broader system descriptor remain P12. Human/browser login,
SAML, Crowd, HTTP-SSO and MFA belong to P21; SCIM provisioning and lifecycle belong to P22.
Their wire inventories below describe target contracts, not working handlers. Current
stub responses and all remaining owners are tracked in the
[capability roadmap](../implementation/capability-roadmap.md) and the
[build status](../implementation/plan.md#build-status).

The status column records the compatibility contract. Current unimplemented security
surfaces include global GPG/passphrase controls and repository signing-key assignment
(P23), trusted Distribution keys (P15, extended in P23/P24), and system encryption,
Access root/CA reads and external secret sources (P38). Keypair CRUD and remote client
certificate CRUD are already implemented; they do not make every signing or certificate
route operational. P20 tracks these distinctions at operation level.

Where a claim names the kind of source behind it, it uses these labels:

| Label | Means |
|---|---|
| **[KB]** | stated in a JFrog KB article at the cited URL |
| **[J]** | recorded in JFrog's public Jira; it counts as 7.x behaviour only when JFrog fixed the issue in 7.x or a JFrog staff comment states it ([sources](README.md#target-version-and-sources)) |
| **[I]** | inferred; not stated by any source, and listed on [#216](https://github.com/weaveplatform/artifactweave/issues/216) |

## Error envelopes

| Service | Envelope |
|---|---|
| Artifactory (`/artifactory/...`) | `{"errors":[{"status":<int>,"message":"<string>"}]}` |
| Access and Projects (`/access/...`) | `{"errors":[{"code":"<string>","message":"<string>"}]}` |

## Legacy Artifactory security API (`/artifactory/api/security/...`)

Every page in this family carries the note that the security APIs are moving to Access
(since 7.49.3) and will be deprecated. They are still called by `artifactory_user`,
`artifactory_group`, `artifactory_permission_target` and the JFrog CLI, so artifactweave
serves thin shims over the same internal model as Access v2. The [7.161.15 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed) repeat
that the user, group, token and permission APIs are being replaced by Access and that the
removal date will be announced in the release notes; the
[deprecations in process](https://docs.jfrog.com/releases/docs/deprecations-in-process) page
says they will first be disabled by default and then removed.

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
| GET | `/api/v2/security/permissions` | 200 | admin, Manage Resources role, or Manage on the target |
| GET | `/api/v2/security/permissions/{name}` | 200 | |
| HEAD | `/api/v2/security/permissions/{name}` | 200 / 404 | existence check |
| POST | `/api/v2/security/permissions/{name}` | **201** | create only; 409 when it exists (jfrog-client-go treats 409 as "exists") |
| PUT | `/api/v2/security/permissions/{name}` | **200** | create or replace; an omitted section (`build`, `releaseBundle`) is deleted. A `build` section without `repositories` targets `artifactory-build-info`, which "cannot be changed", and a `releaseBundle` section `release-bundles` ([createorreplacepermissiontargetv2](https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2)). A Manage holder who is neither an admin nor a Manage Resources holder changes only actions, granting only those it holds and never `manage` (`exclude-manage`, the default), and only in the entries of "other users and groups": never its own entry, nor, **[I]**, the entry of a group it belongs to ([permissions](https://docs.jfrog.com/administration/docs/permissions)) |
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
| POST | `/api/security/users/authorization/expirePassword/{userName}` | 200 | admin; no body; deprecated from 7.49.3 but still served ([deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis), "Expire Password for a Single User"). `text/plain` `<userName>'s credentials have been expired`, and 404 for an unknown user, as the unexpire form documents. **[I]** OSS 6.20 `SecurityUserResource` answers that text, but 400 for an unknown user and 400 "Password expiration is not enforced, please enable it first" while the expiration policy is off; artifactweave expires whatever the policy, as every expiry does (decision 0005) |
| POST | `/api/security/users/authorization/changePassword` | 200 | body `{userName,oldPassword,newPassword1,newPassword2}`; "Admin can apply this method to all users, and each (non-anonymous) user can use this method to change their own password" (same page, "Change Password"). `oldPassword` must verify for every caller, an administrator included: the REST API has no reset without it (**[J]** [RTFACT-13313](https://jfrog.atlassian.net/browse/RTFACT-13313), open). An expired `oldPassword` verifies, and a user whose password expired may send the change with it. `text/plain` "Password has been successfully changed"; a wrong or mismatched password is 400 **[I]** (OSS 6.20 `SecurityServiceImpl.changePassword`, `canUserLogin`). artifactweave also applies the password policy, refuses an access token scoped more narrowly than its user (403) as `PUT /access/api/v2/users/{username}/password` does, counts a wrong `oldPassword` against the lock policy, and answers 404 for an administrator naming an unknown user |
| POST | `/api/security/users/authorization/expirePasswordForAllUsers` | 200 | admin; no body; every internal password, the caller's included; deprecated (same page, "Expire Password for All Users"). `text/plain` "Credentials have been successfully expired for all users" **[I]** (OSS 6.20) |
| POST | `/api/security/users/authorization/unexpirePassword/{userName}` | 200 | **[I]** JFrog's [deprecations table](https://docs.jfrog.com/releases/docs/artifactory-deprecations) says this endpoint "has been removed from Artifactory REST APIs and the UI", with no version, while its [reference page](https://docs.jfrog.com/administration/reference/unexpirepasswordforsingleuser) still documents it. artifactweave serves it; unconfirmed for 7.x |
| GET / PUT | `/api/security/configuration/passwordExpirationPolicy` | 200 | `{"enabled":true,"passwordMaxAge":60,"notifyByEmail":true}`. The docs show quoted strings but `artifactory_password_expiration_policy` decodes `bool` and `int64`, so real JSON types are emitted |
| POST | `/api/security/token` | 200 | legacy create or refresh, form-encoded: `grant_type` (`client_credentials` default, `refresh_token`), `username`, `scope` (legacy forms `member-of-groups:<g>`, `api:*`, `jfrt@<id>:admin`; `member-of-groups:*` authenticates as a copy of the real user, admin flag included, per OSS 6.20 `AccessTokenAuthenticationProvider.populateGroups`, and is the scope `jf` requests when it swaps a configured user's password for a token, `jfrog-cli-core` `utils/config/tokenrefresh.go`), `expires_in`, `refreshable`, `audience`, `refresh_token`, `access_token`. Not on JFrog's deprecation page; `jf` still implements it |
| GET | `/api/security/token` | 200 | admin only, "Requires an admin user" ([deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis), "Get Tokens"); every user's tokens, `{"tokens":[{token_id,issuer,subject,expiry,refreshable,issued_at}]}` |
| POST | `/api/security/token/revoke` | 200 | admin only, even for the caller's own token, "Requires an admin user" (same page, "Revoke Token"); form `token` or `token_id`; 200 even if already revoked |
| GET | `/api/system/security/certificates` | 200 | remote-repository client certificates: `[{certificateAlias,issuedTo,issuedBy,issuedOn,validUntil,fingerPrint}]` |
| POST | `/api/system/security/certificates/{alias}` | 200 | PEM body, `text/plain`; response `{status,message[]}` |
| DELETE | `/api/system/security/certificates/{alias}` | 200 | |
| PUT / GET | `/api/gpg/key/public`, `/api/gpg/key/private`, `/api/gpg/passphrase` | 200 | `text/plain`; passphrase via header `X-GPG-PASSPHRASE` |
| POST | `/api/security/keypair` | **201** | `{pairName,pairType:"RSA"\|"GPG",alias,privateKey,publicKey,passphrase,vaultKey,vaultPublicKey{path,key}}`; admin only; creates the pair, or replaces a pair of the same name ([createkeypair](https://docs.jfrog.com/administration/reference/createkeypair)). Repositories refer to a pair by name, so those using a replaced pair sign with its new material **[I]** |
| GET | `/api/security/keypair` | 200 | list; admin only |
| PUT | `/api/security/keypair` | 200 | admin only; replaces the pair named in the body |
| POST | `/api/security/keypair/verify` | 200 | admin only; `text/plain` `Key was verified.` ([verifykeypair](https://docs.jfrog.com/administration/reference/verifykeypair)) |
| GET | `/api/security/keypair/{name}` | 200 | only `{pairName,pairType,alias,publicKey}`, never the private key or passphrase; any authenticated user, or anonymous when anonymous access is on ([getkeypair](https://docs.jfrog.com/administration/reference/getkeypair)) |
| DELETE | `/api/security/keypair/{name}` | 200 | admin only; `text/plain` `OK`; the page documents 200, 401, 403, 404 and 500 and no refusal of a pair a repository uses ([deletekeypair](https://docs.jfrog.com/administration/reference/deletekeypair)). artifactweave deletes such a pair and clears it from the repositories that name it, in one transaction **[I]** ([#473](https://github.com/weaveplatform/artifactweave/issues/473)) |
| GET | `/api/security/keypair/public/repositories/{repo}` | 200 | `text/plain` public key assigned to a repository, "a text/plain attachment" fetched with `curl -O -J`; any authenticated user, or anonymous when anonymous access is on, with no permission on the repository ([getkeypairpublickeyperrepository](https://docs.jfrog.com/administration/reference/getkeypairpublickeyperrepository)). `Content-Disposition: attachment; filename=<alias>.rsa.pub`, or `.gpg.pub` for a GPG pair **[I]**: the jfrog/artifactory provider documents the pair's `alias` as one that "Will be used as a filename when retrieving the public key via REST API" (`docs/resources/keypair.md` at `master`, read 2026-10-19), and the rest of the name is inferred ([#473](https://github.com/weaveplatform/artifactweave/issues/473)) |
| POST | `/api/v2/repositories/{repo}/keyPairs`, `.../primary`, `.../secondary` | 200 | body is the key-pair name, `text/plain` |
| POST | `/api/security/keys/trusted` | **201** | distribution public keys; GET returns `{"keys":[{kid,alias,fingerprint,key,issued_on,issued_by,valid_until}]}`; DELETE `.../{key_id}` 204 or 200 |
| POST | `/api/system/encrypt`, `/api/system/decrypt` | 200 | `DONE` |
| GET | `/api/system/service_id` | 200 | `jfrt@<uuid>`, `text/plain` |

No SSH-key endpoints exist in current docs or clients. The old `/api/ssh/key` page is gone.

**A `+` in a name stands for a space.** In a user, group or permission name in the path of
`/api/security/users/{userName}`, `/groups/{groupName}`, `/permissions/{name}`,
`/api/v2/security/permissions/{name}` and their sub-paths (`authorization/expirePassword/{userName}`,
`unexpirePassword/{userName}`, the v2 `users/{userName}` and `groups/{groupName}`), "a plus
symbol ('+') is automatically placed when there is a space", because
`artifactory.security.api.plus.insteadof.space` is `true` by default
([deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis),
"Get Users", "Update User", "Delete User" and the group sections;
[deletepermissiontarget](https://docs.jfrog.com/administration/reference/deletepermissiontarget),
[createorreplacepermissiontargetv2](https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2),
[deletepermissiontargetv2](https://docs.jfrog.com/administration/reference/deletepermissiontargetv2)).
So `GET /api/security/permissions/Any+Remote` reads the seeded `Any Remote`. With the property
on, 7.x also turns `%2B` into a space: a group named `+test group` could not be read, and
JFrog's answer was the property (**[J]**
[RTFACT-27304](https://jfrog.atlassian.net/browse/RTFACT-27304), 7.41.4). artifactweave keeps
`%2B` a plus sign, so a name that contains one stays reachable, and its listings' `uri` escape
a plus as `%2B` (DV-075 in the [deviations register](../implementation/deviations.md)).

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
`last_logged_in` is `null` for a user who never logged in, where it was
`1970-01-01T00:00:00.000Z` before 7.98.7 ([7.98.7 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed)); artifactweave answers
`null` too ([#350](https://github.com/weaveplatform/artifactweave/issues/350)). Access answers
compact JSON from 7.117.5 ([7.117.5 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed)) and is served only at `/access` from 7.90.5,
no longer at `/artifactory/api/access` ([7.90.5 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed)); artifactweave does both. From SaaS 7.164.0
([release notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71640-saas)), a non-admin with a scoped token may read user, group and permission
details and project groups and roles, which the `system:` scopes below already allow; that
change is not yet in self-managed 7.161.32.

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
| GET | `/access/api/v2/permissions` | 200 | admin, Manage Resources role or `system:permissions:r`; query `cursor`, `limit` (1 to 99999), `user` (csv, matched case-insensitively), `group` (csv, matched exactly), `include_inherited_groups` (default true), `expand` (needs `user` or `group`). The [reference page](https://docs.jfrog.com/administration/reference/getpermissions) gives `user`, `group`, `include_inherited_groups` and `expand` as available only from Artifactory 7.210.0, later than 7.161.32 and SaaS 7.174.0; artifactweave serves them already ([0006](decisions/0006-authorization-native-permission-model.md)) |
| POST | `/access/api/v2/permissions` | **201** | 409 on conflict |
| GET | `/access/api/v2/permissions/{name}` | 200 | |
| DELETE | `/access/api/v2/permissions/{name}` | 204 | |
| GET / PUT / PATCH / DELETE | `/access/api/v2/permissions/{name}/{resourceType}` | 200 / 204 | admin only, GET included: "Requires admin privileges" ([getpermissionresource](https://docs.jfrog.com/administration/reference/getpermissionresource)), so a `system:permissions:r` token may not read one section; `resourceType` in `artifact`, `build`, `release_bundle` (the provider also uses `destination` and `pipeline_source`) |

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
| POST | `/access/api/v1/tokens` | **200** | create or refresh; JSON or form-urlencoded; non-admins can only create identity tokens; `artifactory_scoped_token` requires exactly 200; Basic credentials need "Enable Token Generation via API" (see [Authentication](#authentication-accepted-on-every-endpoint), item 7) |
| GET | `/access/api/v1/tokens` | 200 | admin sees all, a user sees their own; `{"tokens":[TokenDetails]}`; `last_used` needs `access_token_last_used_enabled` (7.108.3+); a `scope` query parameter filters by scope from 7.133.3 ([7.133.3 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed), SaaS 7.126.0), not yet on the [reference page](https://docs.jfrog.com/administration/reference/gettokens), which says reference tokens are listed from 7.133.8; artifactweave keeps the tokens whose whole scope, or one of its space-separated scopes, is exactly the string, and lists tokens issued with a reference token like any other ([#351](https://github.com/weaveplatform/artifactweave/issues/351)); the matching rule is **[I]**, since JFrog says only "token results for a specific scope, such as group" |
| GET | `/access/api/v1/tokens/{tokenId}` | 200 / 404 | `me` is the current token; the provider removes the resource from state on 404 |
| DELETE | `/access/api/v1/tokens/{tokenId}` | 200 | revoke by id; `me` allowed; a token that is not revocable answers 400 **[KB]** ([000005622](https://jfrog.com/help/r/artifactory-how-to-troubleshoot-failed-to-revoke-tokens-when-trying-to-revoke-a-token-through-the-ui)) |
| DELETE | `/access/api/v1/tokens/revoke` | 200 | admin; body `{"token":"<jwt or reference>"}` returns `{"revoked_token_id":"..."}`; 404 for a token "not found or not revocable" ([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue)) |
| GET / PUT | `/access/api/v1/config/token/default_expiry` | 200 | `{"default_token_expiration":31536000}`, one year until an administrator sets it (Defaults and limits below) |
| POST | `/access/api/v1/service_trust/pairing/{useCase}` | 200 | `mission-control`, `bridge`, `platform-federation`; currently 501. P13/P14 own artifactweave peer/platform trust, P37 external interoperability, and P40 estate/bridge integrations |

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
The [reference page](https://docs.jfrog.com/administration/reference/gettokens) has no
`audience` field, but the
[deprecated JFrog APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis) page
lists `audience`, `description`, `scope`, `project_key` and `last_used` as additional fields of
the Access response. Which holds for 7.x is on
[#216](https://github.com/weaveplatform/artifactweave/issues/216). `subject` has the form
`jfac@<service-id>/users/<name>`.

Defaults and limits: `expires_in` defaults to the value of `default_expiry`. The Access
configuration reference gives one year (`token.default-expiry: 31536000`,
[supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)),
as does the JFrog CLI, while the
[access-tokens page](https://docs.jfrog.com/administration/docs/access-tokens) says 3600 s;
decision 0005's [7.x check](decisions/0005-authentication-and-tokens.md#artifactory-7x-check)
records the maintainer's choice of one year. `0` means never expires: "0 for non-expirable
(admin only)", and a refused `expires_in` is 403, "The requested token details are forbidden"
([createtoken](https://docs.jfrog.com/administration/reference/createtoken)), although the
[recommended configurations](https://docs.jfrog.com/administration/reference/recommended-configurations)
page says users may set any value while `token.max-expiry` is 0;
tokens with an expiry below the `revocable-expiry-threshold` (default 21600 s) are not
revocable unless `force_revocable` or, for a JWT, `refreshable`
([revoketokenbyvalue](https://docs.jfrog.com/administration/reference/revoketokenbyvalue));
tokens below `persistent-expiry-threshold` (default 10800 s) are not stored at all; refresh
grace `token.refresh-expiry` 24 h; `token.max-expiry` 0 means unlimited; `scope` at most 500
characters, `username` at most 255, `description` at most 1024.

Users that do not exist: an administrator may name any `username`, and "If the user specified
does not exist, the system will create a corresponding transient user", which "exists only for
the duration of that session and is not saved to the database"
([access-tokens page](https://docs.jfrog.com/administration/docs/access-tokens), "Support
Authentication for Non-existing Users" and "Transient Users and Auto Join Groups"). A groups
scope acts through "Explicitly mapped groups only", and `applied-permissions/user` through those
"plus any local Auto Join groups". Non-administrators "can only create tokens with themselves as
the Subject". [RTFACT-30181](https://jfrog.atlassian.net/browse/RTFACT-30181) shows a transient
user's reference token working on 7.68.14, and
[RTFACT-23934](https://jfrog.atlassian.net/browse/RTFACT-23934) a transient user's token minting
further time-limited tokens on 7.5.7, under the user `token:<transient username>`; its reporter
warned that such a chain "can be refreshed indefinitely". artifactweave lets a transient user
mint for itself, with its own scope and transient identity, a token that expires no later than
its own and refreshes only if its own does
([decision 0005](decisions/0005-authentication-and-tokens.md#decision), DV-093,
[#456](https://github.com/weaveplatform/artifactweave/issues/456)).

### Scope grammar

| Scope | Meaning |
|---|---|
| `applied-permissions/user` | the subject's own permissions (identity token, the default) |
| `applied-permissions/admin` | platform admin, whatever the subject: "It is possible to provide a username that does not exist in the instance, and the token will be granted admin permissions" ([create scoped token](https://docs.jfrog.com/administration/reference/create-scoped-token)) |
| `applied-permissions/groups:"g1","g 2"` | the union of the named groups' permissions |
| `applied-permissions/roles:<project>:developer,"Project Admin"` | project roles |
| `artifact:<repo or *>[/<ant path>]:<actions>` | actions `r,w,d,a,x,s,m` or `*` |
| `build:<build-info repo or *>[/<ant path>]:<actions>` | the build resource, matched on a run's document path; `build:artifactory-build-info/**:r` lets a non-admin run AQL `builds.find()` (https://docs.jfrog.com/artifactory/docs/aql-query-execution, research 22 §1.3) |
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
(or trailing `=`) fails silently and the CLI prompts for a username. artifactweave escapes
`>`, `?`, `~`, DEL and every non-ASCII rune inside the payload's JSON strings as `\u`
sequences, which keeps `-` and `_` out of the encoding by construction, and encodes without
padding (`cliSafe`, `internal/auth/token/jwt.go`; decision 0005, "As implemented (#74)").

### Roles, environments, OIDC, SCIM, external auth, config

| Method | Path | Status | Notes |
|---|---|---|---|
| POST | `/access/api/v1/roles` | **201** | body `{name,description,type:"CUSTOM_GLOBAL",environments[],actions[]}`; response `{code,message}`, not the role |
| GET | `/access/api/v1/roles` | 200 | `type` enum `ADMIN | PREDEFINED | CUSTOM_GLOBAL` |
| GET / PUT / DELETE | `/access/api/v1/roles/{role}` | 200 | |
| POST | `/access/api/v1/environments` | **201** | `{"name":"..."}`, at most 32 characters, case-insensitive; built-ins `DEV`, `PROD`; at most 10 global environments by default (`environments.global-limit` in [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)), and the next answers 400 **[I]** |
| GET | `/access/api/v1/environments` | 200 | |
| POST | `/access/api/v1/environments/{name}/rename` | 200 | `{"new_name":"..."}` |
| DELETE | `/access/api/v1/environments/{name}` | 204 | 404 when absent |
| POST / GET | `/access/api/v1/oidc` | **200** | create returns 200, not 201; body `{name,issuer_url,audience,description,provider_type,token_issuer,enable_permissive_configuration,use_default_proxy,azure_app_id,organization,project_key}`; `provider_type` in `generic | GitHub | GitHub Enterprise | Azure`; `organization` is required for GitHub types |
| GET / PUT / DELETE | `/access/api/v1/oidc/{name}` | 200 | |
| POST / GET | `/access/api/v1/oidc/{provider}/identity_mappings[?project_key=]` | 200 | `{name,description,priority,claims{...},token_spec{username,scope,audience,expires_in,username_pattern,groups_pattern,self_revocable}}` |
| PUT / DELETE | `/access/api/v1/oidc/{provider}/identity_mappings/{mapping}` | 200 | |
| POST | `/access/api/v1/oidc/token` | 200 | **no auth**; `{grant_type:"urn:ietf:params:oauth:grant-type:token-exchange",subject_token_type:"urn:ietf:params:oauth:token-type:id_token",subject_token,provider_name,project_key,identity_mapping_name,include_reference_token,application_key,job_id,run_id,repo,revision,branch}` returns `{access_token,token_type,expires_in,issued_token_type,username,reference_token}` |
| GET | `/access/api/v1/scim/v2/ResourceTypes`, `/Schemas[/{id}]`, `/ServiceProviderConfig` | 200 | `application/scim+json`; currently 501, P22 |
| GET / POST | `/access/api/v1/scim/v2/Users`, `/Groups` | 200 / 201 | read needs admin or `system:identities:r`, write `system:identities:w`; currently 501, P22 ([ADR 0019](decisions/0019-human-authentication-tiers.md)) |
| GET / PUT / PATCH / DELETE | `/access/api/v1/scim/v2/Users/{id}`, `/Groups/{id}` | 200 / 204 | currently 501, P22 |
| GET / POST | `/access/api/v1/saml` | 200 | plus `/saml/{name}` and `/saml/general_settings` `{enable,certificateIsPresent}`; currently 501, P21 |
| GET / POST | `/access/api/v1/ldap/settings` | 200 | plus `/settings/{key}`, `/settings/test`; `/ldap/groups[/{name}]`, `.../refresh`; currently 501, P12 |
| GET / PUT | `/access/api/v1/httpsso`, `/access/api/v1/crowd` | 200 | currently 501, P21 |
| GET / PUT | `/access/api/v1/config/security/authentication/basic_authentication_enabled` | 200 | `{"enabled":true}` |
| PUT / PATCH | `/access/api/v1/config/security/password_policy` | **204** | `{uppercase,lowercase,digit,special_char,length,not_match_old}`; PUT overwrites all, PATCH is partial; no body |
| GET | `/access/api/v1/cert/root`, `/access/api/v1/cert/ca` | 200 | `text/plain`; currently 501, P38 |
| GET / PUT / DELETE | `/access/api/v1/vault/configs[/{key}]` | 200 | currently 501, P38 |
| PUT / GET / DELETE | `/access/api/v1/aws/iam_role[/{username}]` | 200 | `{username,iam_role}`; currently 501, P38 |
| GET / POST | `/access/api/v1/system/federation` | 200 | Access Federation, Enterprise+; currently 501, P14 and P37 external interoperability |
| POST | `/access/api/v1/system/backup/export`, `/import` | 202 | self-hosted; currently 501, P36 |

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
   From 7.111.4 an administrator blocks existing API keys with a platform setting that is
   off by default ([7.111.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed)); artifactweave never holds API keys, so none exist to
   block.
4. **Public entry points** include ping, the Access version, readiness, liveness, router
   health and the usage sinks. OIDC token exchange verifies the provider token; Terraform
   discovery/backend ping are public, while signed download/upload URLs authorize through
   their capability. These exceptions do not grant anonymous repository access: that
   needs `security.anonAccessEnabled` and a permission granting `anonymous`.
   `GET /artifactory/api/system/version` is not among
   them: since 7.35.1 it refuses callers without credentials
   (**[J]** [RTFACT-26414](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26414);
   [research 01](01-jfrog-api-landscape.md#authentication-schemes)).
5. Basic auth can be disabled platform-wide through
   `/access/api/v1/config/security/authentication/basic_authentication_enabled`.
   Failed Basic logins also suspend the user name for a moment: "the system will temporarily
   suspend (with a 403 HTTP status code) that user's account for a brief period of time during
   which additional login attempts will be ignored", the suspension growing with each failure
   "until it reaches a maximum of 60 seconds"
   ([security configuration](https://docs.jfrog.com/administration/docs/security-configuration),
   "Temporary Login Suspension"). The Access `security.user-lock-policy` sets it:
   `base-login-delay-millis: 1000`, `max-login-delay-millis: 60000` and `seconds-to-unlock: 1`
   ([supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)),
   and from 7.90 the UI changes or disables it. The suspension starts at the second failure in
   a row, and the right password is refused while it lasts **[KB]**
   ([Artifactory intermittent 403 errors](https://jfrog.com/help/r/artifactory-intermittent-403-errors-while-working-with-artifactory/artifactory-intermittent-403-errors-while-working-with-artifactory),
   whose log line reads "This request is blocked due to recurrent request failures, please try
   again in 2 second(s)"). The REST body says "login failures" and "seconds": 7.61.1 answers
   `{"errors":[{"status":403,"message":"This request is blocked due to recurrent login
   failures, please try again in 4 seconds"}]}` (**[J]**
   [RTFACT-29934](https://jfrog.atlassian.net/browse/RTFACT-29934)), and 7.27.6 "please try
   again in 1 seconds", plural for one second too (**[J]**
   [RTFACT-26575](https://jfrog.atlassian.net/browse/RTFACT-26575)). artifactweave answers the
   body's wording with the time left rounded up
   ([#493](https://github.com/weaveplatform/artifactweave/issues/493)). The suspension moved
   into Access in 7.98 ([7.98.7 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed));
   that Access kept the wording is **[I]**. A cluster applies it on each node separately.
   artifactweave enforces it (`internal/auth/suspension.go`): the delay is the base doubled for
   each failure past the threshold, capped at the maximum, as Artifactory 6.x's
   `loginBlockDelay << (attempts - allowed)` did **[I]**; the check comes after the lock and
   before a password is hashed; attempts refused while suspended are not counted; a name that
   does not exist is suspended alike; a login whose password verifies clears the failures; and
   tokens are not suspended **[I]**.
   A locked account, which the user lock policy locks after too many wrong passwords, is
   refused whatever password it sends: 7.x checks the lock before the password, logging
   "Checking if user is not locked" and then "User is locked, denying login." with no failed
   attempt registered (**[J]** [RTFACT-29453](https://jfrog.atlassian.net/browse/RTFACT-29453),
   7.31.10 to 7.55.6). artifactweave answers it 403 "User is Locked.\nContact System
   Administrator to Unlock The Account." before the suspension and the password, and counts
   nothing ([#475](https://github.com/weaveplatform/artifactweave/issues/475)). The status and
   message are OSS 6.20's: `ArtifactoryBasicAuthenticationEntryPoint` answers a
   `LockedException` 403 with the `UserLockedException` message **[I]**.
6. The Docker token flow is described in [04-package-protocols.md](04-package-protocols.md);
   `/artifactory/api/docker/<repo>/v1|v2/auth` returns `{"auth":"<base64 user:pass>","email":"..."}`.
7. Basic credentials create tokens through `POST /access/api/v1/tokens` only once an
   administrator turns on **Enable Token Generation via API**, the Access setting
   `token.allow-basic-auth`, which is `false` by default.
   - [createtoken](https://docs.jfrog.com/administration/reference/createtoken) says "If you use
     basic credentials, you must enable creation of tokens in the JFrog Platform UI".
   - [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)
     lists `allow-basic-auth: false # when enabled, allow basic authentication for create token
     api`.
   - [security configuration](https://docs.jfrog.com/administration/docs/security-configuration)
     (2026-08-20) describes the UI switch: "allow users logged in through basic authentication
     to generate tokens with the API".
   - No REST API sets it: **[J]**
     [RTFACT-29875](https://jfrog.atlassian.net/browse/RTFACT-29875), still open, asks for one.
     The setting lives in the UI and in `access.config.patch.yml`.
   - Access answers Basic credentials it does not take on its APIs with
     `{"errors":[{"code":"UNAUTHORIZED","message":"Unsupported authentication method Basic"}]}`
     **[KB]** ([what causes "Unsupported authentication method Basic"](https://jfrog.com/help/r/artifactory-what-causes-the-error-message-unsupported-authentication-method-basic-to-appear-when-using-a-rest-api)).
   - The legacy `POST /artifactory/api/security/token` takes Basic credentials:
     `jf c add` with a password swaps it for a token there (jfrog-cli-core
     `utils/config/tokenrefresh.go`, `createTokensForConfig`), and `jf rt atc` with a password
     works where `jf atc` refuses it (jfrog-cli issue
     [2547](https://github.com/jfrog/jfrog-cli/issues/2547)).

Built-in principals that Artifactory ships and artifactweave seeds at first start:
`admin`, `anonymous`, the `readers` group, and the `Anything` and `Any Remote` permission
targets. Since 7.84.3 neither target grants `anonymous` by default
([permissions](https://docs.jfrog.com/administration/docs/permissions); "Changes to
Anonymous Access" in the
[7.84.10 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed)).
`anonAccessEnabled` lets requests without credentials act as `anonymous`, which then reads
only what a permission target grants it. JFrog's advice is a new permission target that
gives `anonymous` read access to the repositories it needs.

artifactweave seeds both without `anonymous` (`internal/auth/seed.go`). `Anything` grants the
`readers` group read on `ANY`. `Any Remote` targets `ANY REMOTE` with the include pattern
`**` and grants no one. **[I]** The pages say only that `anonymous` is removed; that no other
user or group takes its place is inferred. A store an earlier artifactweave seeded loses the
grant at its next start, as decision
[0006](decisions/0006-authorization-native-permission-model.md#seeded-permissions-383) records.

## Tier notes

Legacy `/api/security/users|groups|permissions` v1, `/api/security/token*` and
`encryptedPassword` remain compatibility shims over the shared model. API key creation is
disabled: access/reference tokens replace it, with evidence and migration guidance in the
[capability roadmap](../implementation/capability-roadmap.md#supersession-register).

Commercial tier and hosting restrictions do not remove capabilities from scope. Access
Federation and Grid/topology belong to P14, with external adapters in P37; release-bundle
permissions belong to P15/P24; log shipping belongs to P38; Mission Control, MyJFrog and
bridge integrations belong to P40; Curation belongs to P27. Their current stub or absent
routes remain unfinished work. SAML, Crowd and HTTP-SSO are P21, and SCIM is P22. Provider
access needed to prove a hosted integration is a recorded phase dependency, never a
successful local imitation of a vendor-owned operation.

Primary URLs: https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt,
https://docs.jfrog.com/administration/reference/jfrog-artifactory-security-api/llms.txt,
https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis,
https://docs.jfrog.com/administration/docs/access-tokens,
https://docs.jfrog.com/user-management/docs/api-key,
https://docs.jfrog.com/user-management/docs/identity-tokens,
https://github.com/jfrog/terraform-provider-platform/blob/main/docs/resources/permission.md,
https://github.com/jfrog/jfrog-client-go/blob/master/auth/authutils.go.

## Artifactory 7.x check

Audited on 2026-10-18 against Artifactory 7.161.32 (SaaS 7.174.0) with the KB, Jira and
client sources the [research README](README.md#target-version-and-sources) lists
([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** The docs.jfrog.com pages cited in the tables above, read as `.md` on 2026-10-18;
the [self-managed](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)
and [SaaS](https://docs.jfrog.com/releases/docs/artifactory-saas-releases) release notes;
KB 000005622; JFrog's public Jira; jfrog-cli and jfrog-client-go at `master`;
terraform-provider-artifactory v12.11.14 and terraform-provider-platform v2.2.11.

**Confirmed for 7.x:** the Access token routes, fields and scope grammar; the shapes and
statuses of the legacy token, user, group, lock-policy and password-expiry APIs; key pair and
certificate shapes; who may read one key pair or a repository's public key, a create that
replaces a pair of the same name, and the verify and delete texts (the key pair rows of the
legacy table, [#407](https://github.com/weaveplatform/artifactweave/issues/407)); Access v2
users, groups and permissions; roles and environments; the basic authentication switch and
the password policy. Every read path and status the two Terraform providers check is met.

**Corrected here:**
- the default token lifetime, and which tokens are revocable or stored (Access tokens v1);
- the revoke statuses for a token that is not revocable (Access tokens v1);
- when the Access permission filters became available (Permissions v2);
- the JWT payload quirk, which described padding the payload rather than escaping it;
- three deprecated password endpoints missing from the legacy table.

**Settled by [#408](https://github.com/weaveplatform/artifactweave/issues/408).** At most 10
global environments exist by default
([createglobalenvironment](https://docs.jfrog.com/administration/reference/createglobalenvironment):
"By default, Artifactory supports up to 10 global environments"; `global-limit: 10` in
[supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)),
and artifactweave refuses the next with 400; `environments.global_limit` changes the limit. The
temporary login suspension (item 5 of
[Authentication](#authentication-accepted-on-every-endpoint)) is enforced, and
`secondsToUnlock` defaults to 1.

**Settled by [#473](https://github.com/weaveplatform/artifactweave/issues/473) and
[#475](https://github.com/weaveplatform/artifactweave/issues/475).** Deleting a key pair a
repository uses answers 200 "OK", as deletekeypair documents no refusal, and the
repositories lose their reference to it. A repository's public key comes as an attachment
with a file name, as getkeypairpublickeyperrepository's `curl -O -J` needs. A locked account
answers 403 whatever password it sends, its lock checked before the password (item 5 of
[Authentication](#authentication-accepted-on-every-endpoint)).

**Settled by [#493](https://github.com/weaveplatform/artifactweave/issues/493).** A suspended
login answers 403 "This request is blocked due to recurrent login failures, please try again
in N seconds", the REST body 7.61.1 and 7.27.6 answer (**[J]** RTFACT-29934 and RTFACT-26575,
item 5 of [Authentication](#authentication-accepted-on-every-endpoint)), where it gave the KB's
log line before.

**Where artifactweave differs.** Decision 0005's
[7.x check](decisions/0005-authentication-and-tokens.md#artifactory-7x-check) lists the token,
account policy, seeding and OIDC differences, and decision 0006's
[7.x check](decisions/0006-authorization-native-permission-model.md#artifactory-7x-check) the
permission ones. The others:
- 7.x differs: `last_used` is recorded and returned only when the
  `access_token_last_used_enabled` and `access_token_last_used_threshold` (900 s) system
  properties are set, which they are not by default
  ([gettokens](https://docs.jfrog.com/administration/reference/gettokens)); artifactweave
  always records it, at most once a minute per token (`internal/auth/token/token.go`), and
  returns it. Kept, as a deviation registered by #378.

The three deprecated password endpoints are served as the legacy table records, and a `+` in a
user, group or permission name in the legacy paths stands for a space, `%2B` excepted, as the
paragraph after the [legacy table](#legacy-artifactory-security-api-artifactoryapisecurity)
records.

**Unconfirmed for 7.x**, for the live check on
[#216](https://github.com/weaveplatform/artifactweave/issues/216):
- The status and message of `POST /access/api/v1/tokens` with a password while
  `token.allow-basic-auth` is off. **[I]** 401 with "Unsupported authentication method Basic",
  from the KB article on Access's answer to Basic credentials it does not take (item 7 of
  [Authentication](#authentication-accepted-on-every-endpoint)).
- Whether `token.allow-basic-auth` also governs the legacy `POST /api/security/token`.
  **[I]** It does not: the JFrog CLI's password configurations create their tokens there,
  which would fail on every default instance if it did.
- The default lifetime of an Access token. **[D]** The configuration reference and the JFrog
  CLI say one year, which artifactweave follows; the access-tokens page says 3600 s (Access
  tokens v1).
- Whether a non-administrator's `expires_in: 0` succeeds while `token.max-expiry` is at its
  default 0. **[I]** It is refused 403, as createtoken's field note and its 403 describe;
  recommended configurations says users may then set any value.
- How the user API answers for a transient user, and how its tokens are revoked. **[I]**
  `GET` and `DELETE /api/security/users/<name>` answer 404, deleting the name revokes nothing,
  and its tokens are listed and revoked through the token API as any other; the access-tokens
  page says only that the user is not saved.
- The lifetime of a legacy `POST /api/security/token` without `expires_in`. **[J]**
  [RTFACT-29239](https://jfrog.atlassian.net/browse/RTFACT-29239) says it rose from one hour
  to one year in 7.46.x, but the issue is open and the statement is not a JFrog staff comment.
  `jf rt atc` sends 3600 itself. artifactweave keeps 3600 s there, whatever `default_expiry`
  says.
- Whether token details carry `audience` and `project_key` (Access tokens v1).
- Whether `unexpirePassword/{userName}` still exists (the legacy table).
- The answer to a correct password that has expired. **[I]** 403 with "Password has expired",
  on every route but the user's own password change, on the Access APIs as on Artifactory's.
  OSS 6.20 `ArtifactoryBasicAuthenticationEntryPoint` answers a `CredentialsExpiredException`
  403, as it does a locked account, and its `AccessUserPassAuthenticationProvider` raises one
  with that message when Access reports `PASSWORD_EXPIRED` (**[J]**
  [RTFACT-17846](https://jfrog.atlassian.net/browse/RTFACT-17846) logs it); for a blocked
  user, the 403 still holds in 7.x (**[KB]**
  [000005331](https://jfrog.com/help/r/artifactory-intermittent-403-errors-while-working-with-artifactory/artifactory-intermittent-403-errors-while-working-with-artifactory)).
- Whether a user may authenticate the legacy `changePassword` with its expired password.
  **[I]** It may: the call is how an expired password is replaced, and OSS 6.20's
  `canUserLogin` accepts an expired old password for it.
- The legacy password endpoints' texts and error statuses: the 200 texts, 400 for a wrong or
  mismatched password, and 404 for an unknown user where OSS 6.20 answers 400 (the legacy
  table). Whether `expirePassword/{userName}` and `expirePasswordForAllUsers` still refuse
  while the expiration policy is off, as OSS 6.20 does.
- Whether `%2B` in a security API name is a space too. **[J]**
  [RTFACT-27304](https://jfrog.atlassian.net/browse/RTFACT-27304) shows it on 7.41.4;
  artifactweave keeps it a plus (DV-075).
- Whether the legacy user has `lastLoggedInMillis` and `offlineMode`. The deprecated-APIs
  page says the Access user drops them, and the
  [legacy user schema](https://docs.jfrog.com/administration/reference/getuserdetails-1)
  lists neither.
- The legacy lock policy on a fresh instance, where Access defaults
  `user-lock-policy.attempts` to 0.
- Whether a non-administrator may read their own Access v2 user.
- The Access permissions cursor, which the page describes both as the first name of the next
  page and as the last name of this one.
- The v2 by-user and by-group views, whose example shows each section's `actions` as a flat
  list.
- How an exclude pattern meets an include pattern in a permission target.
- The characters allowed in environment names; only the length and case are documented.
- The global environment limit. **[I]** It counts `DEV` and `PROD`, and the one past it
  answers 400 "at most 10 global environments may exist", the page's only status for invalid
  input; the page gives no message. The
  [custom global environment guide](https://docs.jfrog.com/administration/docs/create-a-custom-global-environment)
  says 30 from 7.111.0, against 10 on the API page and in the Access configuration reference,
  which artifactweave follows.
- The temporary login suspension's growth and scope. **[I]** The delay doubles from the base
  at each failure past `maxLoginDelayIncorrectAttempts` (2), as in 6.x; failures are forgotten
  a day after the last one; a threshold of 0 or 1 suspends from the first failure; only Basic
  logins with a password are suspended, not tokens; and the 403 body keeps the wording 7.61.1
  answers since the suspension moved into Access in 7.98.
- What `seconds-to-unlock` does in 7.x. **[I]** artifactweave stores and returns it, default 1,
  and a locked user stays locked until an administrator unlocks them, as the security
  configuration page says.
- What the seeded `Any Remote` grants on a fresh 7.x instance, which the pages describe only
  as without `anonymous`.
- The status of `POST /api/security/keypair` when it replaces a pair. **[I]** 201, as for a
  new pair: [createkeypair](https://docs.jfrog.com/administration/reference/createkeypair)
  documents no other success status.
- What a replaced pair does to the repositories that use it. **[I]** They keep referring to
  it by name and sign with the new material, as after `PUT /api/security/keypair`.
- What deleting a pair a repository uses does. **[I]** It succeeds, as deletekeypair lists no
  refusal, and the repositories lose their `primaryKeyPairRef` or `secondaryKeyPairRef` to it
  in the same transaction, so none names a pair that does not exist. That old Artifactory
  removed a pair from a repository only by deleting the pair (**[J]**
  [RTFACT-2807](https://jfrog.atlassian.net/browse/RTFACT-2807)) points the same way.
- The file name of a repository's public key. **[I]** `<alias>.rsa.pub`, or `<alias>.gpg.pub`
  for a GPG pair, sent as `Content-Disposition: attachment; filename=...`.
- The status and message for a locked account. **[I]** 403 "User is Locked.\nContact System
  Administrator to Unlock The Account.", from OSS 6.20's entry point and
  `UserLockedException`; no 7.x capture of the response is published.
