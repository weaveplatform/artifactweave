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
described here. P12 implements LDAP connections, bind and group synchronization
(#1832–#1834) and the broader system descriptor. Human/browser login,
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
| PUT / GET | `/api/gpg/key/public`, `/api/gpg/key/private`, `/api/gpg/passphrase` | 200 | `text/plain`; passphrase via header `X-GPG-PASSPHRASE`. Each write is admin only and sets one field of the `default-gpg-key` pair, which signs Debian, RPM and Opkg metadata for repositories naming no pair; the read answers 404 until a public key is set ([instance-gpg-key.md](instance-gpg-key.md), #1654) |
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

Source indexes: https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt,
and, for Replace or Create a Group only,
https://docs.jfrog.com/administration/reference/jfrog-platform-groups-api/llms.txt.
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

**User names are matched without regard to case and stored in lower case**
([#1432](https://github.com/weaveplatform/artifactweave/issues/1432)). The
[SCIM](https://docs.jfrog.com/administration/docs/scim) page (7.17.0+) says "User names are the
unique identifiers for users in Access. User names are case-insensitive";
[Get Permissions](https://docs.jfrog.com/administration/reference/getpermissions) matches
"Usernames ... case-insensitively, group names exactly"; and
[RTFACT-29660](https://jfrog.atlassian.net/browse/RTFACT-29660) (7.55.13) says a user created
"with capitalization" is stored lower-case, as RTFACT-7243, RTFACT-9966 and RTFACT-12337 say for
earlier versions and OSS 6.20 `SecurityServiceImpl` does on create and update **[I]**. So
artifactweave folds every user name it is given, on both APIs: `POST /access/api/v2/users` and
the legacy `PUT /api/security/users/Dan` store `dan`; `BOB` names `bob` in every user path,
including a legacy body's `name`, in Basic logins (a password, an encrypted password or an
access token, with the lock count and suspension shared) and the Basic name of Conan's
`users/authenticate` exchange, which the Conan client sends as typed, in group `members` and
`userNames`, in permission grants and the `users/{userName}` permission view, in unlocks,
password expiry and changes, in the API restrictions, and in the user a token is minted for or
refreshed by.
The built-in `admin` and `anonymous` stay protected in any case. JFrog bugs that split by case
are not followed: RTFACT-29660 (`unlockUsers` with `TEST_user` does nothing, and failed logins
counted apart), RTFACT-23893 (`"userNames":["TESTUSER"]` adds nobody), RTFACT-10141 (an API key
or encrypted password with an upper-case name, fixed in 7.17.2) and
[RTFACT-23454](https://jfrog.atlassian.net/browse/RTFACT-23454) (an access token with a Basic
name in another case, "Token principal mismatch"). A user row stored in mixed case before this
rule is not rewritten (the preview has no data upgrades); it is no longer reached by name and
is recreated under the lower-case name. No source settles these, all **[I]**:

- a found user answers with its stored name, so `GET /access/api/v2/users/BOB` shows `bob`;
- `POST /access/api/v2/users` with `BOB` beside `bob` is 409 "User 'bob' already exists", and
  the legacy `PUT` of `DAN` replaces `dan` (201);
- a text answer repeats the request's spelling ("User 'BOB' was unlocked"), as does the 404 of
  an unknown name;
- a permission naming one user in two spellings holds the union of their actions;
- a token minted for a user that does not exist (transient) is minted for the folded name,
  while a transient or OIDC token issued before keeps the subject's spelling.

**An unknown group in a user request** (`POST /access/api/v2/users`, `PATCH
/access/api/v2/users/{username}/groups`, the legacy user `PUT` and `POST`) answers 400
"Group 'nope' does not exist", and an unknown member in a legacy group `PUT` or `POST` 400
"User 'ghost' does not exist", worded by the identity service and the same on every store
([#1433](https://github.com/weaveplatform/artifactweave/issues/1433)), as the permission
routes word theirs. [Add or Remove User from Groups](https://docs.jfrog.com/administration/reference/updateusergroups)
documents only 400 "Bad Request - Invalid input" and [Create a User](https://docs.jfrog.com/administration/reference/createuser)
400 for an invalid object; the message is **[I]**. The Access v2 group routes keep their 404
for an unknown member.

### Groups v2

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v2/groups` | 200 | query `limit`, `cursor`, `descendingOrder`, `groupName`, `role`, `resourceType`, `resourceName`, `projectKey`; response `{"groups":[{group_name,uri}],"cursor":"..."}` |
| POST | `/access/api/v2/groups` | **201** | `members` and `import_from_ldap_group_setting` are mutually exclusive; 409 on conflict; `platform_group` requires 201; an import is described in [LDAP groups](#ldap-groups) |
| GET | `/access/api/v2/groups/{name}` | 200 | |
| PUT | `/access/api/v2/groups/{name}` | **201** or 200 | creates the group (201) or replaces it (200); a body `name` that differs from the path answers 400; a replacement resets every field it leaves out and replaces `members`, keeping `realm` and `realm_attributes` ([replacegroup](https://docs.jfrog.com/administration/reference/replacegroup)) |
| PATCH | `/access/api/v2/groups/{name}` | 200 | `realm`, `realm_attributes` and `members` are not updatable here |
| DELETE | `/access/api/v2/groups/{name}` | **204** | `platform_group` requires 204 |
| PATCH | `/access/api/v2/groups/{name}/members` | 200 | `{"add":[...],"remove":[...]}` returns `{"members":[...]}` |

Create body: `{name,description,auto_join,admin_privileges,external_id,import_from_ldap_group_setting,members[],reports_manager,watch_manager,policy_manager,policy_viewer,manage_resources,manage_webhook}`;
details add `realm` and `realm_attributes`. The `roles` field needs Artifactory ≥7.128.0;
there is no other version gate on groups.

**Replace or Create a Group moved; JFrog did not withdraw it**
([#1434](https://github.com/weaveplatform/artifactweave/issues/1434)). On 2026-11-13 the
page answered 404 and left the JFrog Platform REST APIs index. It came back the same day at
the same URL (`updatedAt` 2026-11-13T10:29:17Z), now titled Replace or Create a Group
rather than Create or Replace a Group, as the only page of a new family,
[JFrog Platform - Groups API](https://docs.jfrog.com/administration/reference/jfrog-platform-groups-api/llms.txt).
Its text, status codes and schemas are the same as on 2026-11-10. The
[SaaS release notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)
announce it under 7.177.2 (9 November 2026) as a new REST API. The self-managed notes, up
to 7.173.18, do not mention it yet, and no deprecation notice names an Access v2 group
route. The [deprecated APIs](https://docs.jfrog.com/integrations/docs/deprecated-jfrog-apis)
page, last edited on 2026-11-09, still says the new API is create-only and points at
`PATCH`; it was written before this route was published. No JFrog client sends the route
yet: `platform_group` creates with `POST` and updates with `PATCH`, while
`artifactory_group` and jfrog-client-go send the legacy `PUT /api/security/groups/{groupName}`.
artifactweave keeps serving it, and the spec oracle now fetches the new family, so an
oracle refresh keeps the route under the parity check.

**Group names keep their case and are compared exactly**
([#1309](https://github.com/weaveplatform/artifactweave/issues/1309)), unlike usernames,
which Access lowercases. [Get Permissions](https://docs.jfrog.com/administration/reference/getpermissions)
says "Usernames are matched case-insensitively, group names exactly"; the
[SAML SSO](https://docs.jfrog.com/administration/docs/saml-sso) group attribute makes "a
case-sensitive match to an existing group"; and the KB article on
[SAML SSO with ADFS](https://jfrog.com/help/r/artifactory-how-do-i-configure-artifactory-saml-sso-with-adfs-2022)
says "Internal Artifactory groups are case sensitive". Only groups imported from LDAP are
lowercased ([Create a Group](https://docs.jfrog.com/administration/reference/creategroup);
the [LDAP](https://docs.jfrog.com/administration/docs/ldap) page asks for lower case when such a
group is created over REST). The clients agree: `platform_group` and `artifactory_group` send
the name as written and read it back unchanged. So, in artifactweave as in 7.x, `Ops` created
beside `ops` is a second group on `POST` and `PUT /access/api/v2/groups` and on the legacy
`PUT /api/security/groups/{groupName}`, and `GET .../groups/OPS` answers 404 on both APIs.
The same exact match holds wherever a group name is matched: a permission's grants, a
user's groups, a group's members, the `group` permission filter and the
`applied-permissions/groups` and `member-of-groups` token scopes. Only the `groupName` list
filter ignores case, as a search. No 7.x source states each of these one by one, so the
create conflict, the lookup and the matches outside the group row are **[I]**.

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
| GET / POST / PUT | `/access/api/v1/ldap/settings` | 200 / **201** | plus `GET` and `DELETE /settings/{key}` (204); see [LDAP connections](#ldap-connections). `POST /settings/test` (LDAP-02) 204 or 400, see [LDAP login](#ldap-login); the group settings `/ldap/groups[/{name}]`, `strategy`, `search`, `import` and `{name}/refresh` (LDAP-03), see [LDAP groups](#ldap-groups) |
| GET / PUT | `/access/api/v1/httpsso`, `/access/api/v1/crowd` | 200 | currently 501, P21 |
| GET / PUT | `/access/api/v1/config/security/authentication/basic_authentication_enabled` | 200 | `{"enabled":true}`; internal passwords only, see [internal password login](#internal-password-login) |
| PUT / PATCH | `/access/api/v1/config/security/password_policy` | **204** | `{uppercase,lowercase,digit,special_char,length,not_match_old}`; PUT overwrites all, PATCH is partial; no body |
| GET | `/access/api/v1/cert/root`, `/access/api/v1/cert/ca` | 200 | `text/plain`; currently 501, P38 |
| GET / PUT / DELETE | `/access/api/v1/vault/configs[/{key}]` | 200 | currently 501, P38 |
| PUT / GET / DELETE | `/access/api/v1/aws/iam_role[/{username}]` | 200 | `{username,iam_role}`; currently 501, P38 |
| GET / POST | `/access/api/v1/system/federation` | 200 | Access Federation, Enterprise+; currently 501, P14 and P37 external interoperability |
| POST | `/access/api/v1/system/backup/export`, `/import` | 202 | self-hosted; currently 501, P36 |

The global-role action vocabulary (25 values) and the project-role vocabulary (32 values,
which omits `READ_POLICIES_SECURITY`) are listed in
[07-webhooks-retention-projects.md](07-webhooks-retention-projects.md).

### LDAP connections

Sources: [create](https://docs.jfrog.com/administration/reference/createldapsettings),
[update](https://docs.jfrog.com/administration/reference/updateldapsettings),
[get all](https://docs.jfrog.com/administration/reference/getallldapsettings),
[get by key](https://docs.jfrog.com/administration/reference/getldapsettingsbykey),
[delete](https://docs.jfrog.com/administration/reference/deleteldapsettings),
[LDAP](https://docs.jfrog.com/administration/docs/ldap), the `jfrog/artifactory` provider
12.11.14 `resource_artifactory_ldap_setting_v2.go` and `resource_artifactory_ldap_setting.go`,
and the 7.161.26 open source descriptor (`base/config/.../security/ldap/LdapSetting.java`,
`SearchPattern.java`). Implemented by LDAP-01
([#1832](https://github.com/weaveplatform/artifactweave/issues/1832)).

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v1/ldap/settings` | 200 | every connection; `[]` when none |
| GET | `/access/api/v1/ldap/settings/{key}` | 200 | 404 when absent, which the provider reads as removed (it also accepts 400) |
| POST | `/access/api/v1/ldap/settings` | **201** | the provider posts to the trailing-slash form and checks 201 |
| PUT | `/access/api/v1/ldap/settings` | 200 | the key is in the body; the provider checks 200 |
| DELETE | `/access/api/v1/ldap/settings/{key}` | 204 | the provider also accepts 404 |

The body is `{key,enabled,ldap_url,user_dn_pattern,email_attribute,auto_create_user,
ldap_poisoning_protection,allow_user_to_access_profile,paging_support_enabled,
search:{search_filter,search_base,search_sub_tree,manager_dn,manager_password}}`. Defaults
come from `LdapSetting` and the provider's schema: `enabled`, `auto_create_user`,
`ldap_poisoning_protection`, `paging_support_enabled` and `search_sub_tree` true,
`email_attribute` `mail`, `allow_user_to_access_profile` false. `search_base` may hold
several bases separated by `|` (the LDAP page). The provider sends every field, leaves out
empty search strings and the password, and keeps the password from its state on read, so
the manager password is write-only: artifactweave never returns it, keeps the stored one
when a request leaves it out, and seals it in `config_secrets`
([ADR 0017](decisions/0017-sealed-columns-and-master-key.md)). Every operation needs an
administrator, an administrator's token included.

Local rules, each **[I]** and listed on [#216](https://github.com/weaveplatform/artifactweave/issues/216):

- `PUT` replaces the connection from the defaults, keeping only an omitted password, and
  answers 404 for an unknown key; a second `POST` of a key answers 400.
- An explicit `""` password clears it; a password needs `manager_dn`, and clearing
  `manager_dn` drops the stored password.
- Validation: the key is not empty and has no `/` or control character; `ldap_url` is
  `ldap://` or `ldaps://` with a host, no credentials, query or fragment, and any path is a
  DN; a connection needs `user_dn_pattern` or `search_filter`, each containing `{0}`;
  DNs parse, the filter's parentheses balance, and `email_attribute` is an attribute name.
- Reads return every field, with `""` for an empty string.

The descriptor's `security.ldapSettings` writes the same connections
([research 06](06-system-configuration-and-descriptor.md#security)).

### LDAP login

Sources: [LDAP](https://docs.jfrog.com/administration/docs/ldap) ("Auto Create System
Users", "Email Attribute", "Customize the LDAP Timeout / LDAP Referral Strategy", "Non-UI
Authentication Cache for LDAP", "Prevent Authentication Fallback to the Local Artifactory
Realm", "Using LDAPS"), [test LDAP connection](https://docs.jfrog.com/administration/reference/testldapconnection),
[security configuration](https://docs.jfrog.com/administration/docs/security-configuration)
("Disable Basic Authentication Method"), and Spring Security's `BindAuthenticator` and
`FilterBasedLdapUserSearch`, on which the 7.161.26 open source LDAP login is built.
Implemented by LDAP-02 ([#1833](https://github.com/weaveplatform/artifactweave/issues/1833)).

- **Who logs in.** A Basic password is checked against every enabled connection. Each
  tries `user_dn_pattern` first (relative to the URL's base DN), then `search_filter`
  under each `|`-separated `search_base`, bound as `manager_dn` when one is set, over the
  subtree or one level as `search_sub_tree` says; the user then binds with the password.
  A search that finds several entries finds none, as `FilterBasedLdapUserSearch` refuses
  them. A wrong, unknown or disabled directory user is refused with 401 like a wrong
  internal password.
- **Local identity.** With `auto_create_user` the first login creates the user "and
  assign[s] them to the default groups": realm `ldap`, the `email_attribute`'s value as
  email, `allow_user_to_access_profile` as `profile_updatable`, no internal password.
  Without it the user acts in the default (auto-join) groups and nothing is stored.
  Later logins refresh the email of an `ldap`-realm user.
- **Timeouts.** Connect 10000 ms, each operation 15000 ms, the documented defaults of
  `artifactory.security.ldap.connect.timeoutMillis` and `socket.timeoutMillis`.
- **Cache.** "Once a user is authenticated, while the authentication data is cached,
  Artifactory uses the cached data rather than querying the external system", for 300
  seconds by default (`artifactory.security.authentication.cache.idleTimeSecs`).
- **LDAPS** verifies the server's certificate against the system's trust; a self-signed
  directory's CA is added to it (on Linux through `SSL_CERT_FILE` or `SSL_CERT_DIR`), as
  JFrog's Java trust store is.
- **Connection test.** `POST /access/api/v1/ldap/settings/test` takes a full `LdapSettings`
  and answers 204 "Test successful", 400 "Test failed", 401 and 403, administrators only.
  It stores nothing.

Local rules, each **[I]** and listed on [#216](https://github.com/weaveplatform/artifactweave/issues/216):

- The internal password is checked before the directories, where JFrog asks the
  directory first and falls back to it. Both orders accept the same passwords as the same
  user; this one keeps the local administrator's login off the directory's network path,
  so it works and is not delayed while every directory is unreachable (decision
  [0019](decisions/0019-human-authentication-tiers.md)). A user with
  `internal_password_disabled` has no fallback, as the documented checkbox gives.
- Connections are asked in key order; the first that accepts wins. A refusal from any
  connection refuses the login even when another is unreachable. A login no directory
  could answer is refused without counting toward the lock policy or the login
  suspension, so an outage locks nobody out; a refusal counts as a wrong password.
- An empty password is refused before any bind, since LDAP accepts a simple bind without
  a password as unauthenticated.
- Referrals are not followed (JFrog's `referralStrategy` defaults to `follow`); a
  referral is a failure of that connection.
- With `ldap_poisoning_protection`, an entry carrying a serialized Java object or JNDI
  reference (RFC 2713 and 2714 object classes such as `javaSerializedObject` and
  `javaNamingReference`, or attributes such as `javaSerializedData`, `javaFactory` and
  `javaCodebase`) is no user; JFrog documents the setting only as filtering out "users
  exposed to vulnerabilities".
- A remembered login lasts only while its connection's revision is current, so a
  disable, update or delete ends it at once rather than after 300 seconds. A principal
  from a directory login, and the tokens and downloads that recheck it, end the same way.
- The directory is asked outside any metadata transaction; one guarded transaction then
  rechecks the connection's revision and the user's status before it creates or updates
  the user, so a connection changed or a user disabled meanwhile leaves nothing stored.
- The connection test answers 400 with `LDAP connection test failed: <step>: <reason>`,
  the step being `connect`, `tls`, `manager bind` or `search`. It binds as the manager and
  reads each search base when a search is configured, otherwise it only connects. A body
  without `manager_password` uses the stored connection's only when its key, URL and
  manager DN match, so a test cannot send a stored password to another server.

### LDAP groups

Sources: [LDAP](https://docs.jfrog.com/administration/docs/ldap) ("Synchronize LDAP Groups",
"Group Mapping Strategies", "Importing LDAP Groups", "Configure LDAP Groups with Microsoft
Entra ID"), [create a group](https://docs.jfrog.com/administration/docs/create-a-group), the
Access API pages
[get all](https://docs.jfrog.com/administration/reference/getallldapgroupsettings),
[create](https://docs.jfrog.com/administration/reference/createldapgroupsettings),
[update](https://docs.jfrog.com/administration/reference/updateldapgroupsettings),
[get by name](https://docs.jfrog.com/administration/reference/getldapgroupsettingsbyname),
[delete](https://docs.jfrog.com/administration/reference/deleteldapgroupsettings),
[strategy](https://docs.jfrog.com/administration/reference/getldapgroupmappingstrategy),
[search](https://docs.jfrog.com/administration/reference/searchldapgroups),
[import](https://docs.jfrog.com/administration/reference/importldapgroups) and
[refresh](https://docs.jfrog.com/administration/reference/refreshldapgroupsettings), the
`jfrog/artifactory` provider 12.11.14 `resource_artifactory_ldap_group_setting_v2.go` and
`resource_artifactory_ldap_group_setting.go`, and the 7.161.26 open source descriptor
(`LdapGroupSetting.java`, `LdapGroupPopulatorStrategies.java`) and services
(`RefreshLdapGroupService`, `GroupMappingStrategyService`, `LdapUtils`). Implemented by
LDAP-03 ([#1834](https://github.com/weaveplatform/artifactweave/issues/1834)).

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/access/api/v1/ldap/groups` | 200 | every group setting; `[]` when none |
| POST | `/access/api/v1/ldap/groups` | **201** | the v2 provider posts to the trailing-slash form, checks 201, then refreshes |
| PUT | `/access/api/v1/ldap/groups` | 200 | the name is in the body; 404 when absent |
| GET | `/access/api/v1/ldap/groups/{name}` | 200 | 404 when absent, which the provider reads as removed (it also accepts 400) |
| DELETE | `/access/api/v1/ldap/groups/{name}` | 204 | 404 when absent, which the provider accepts; imported groups stay |
| GET | `/access/api/v1/ldap/groups/strategy?strategy=` | 200 | `{groupKeyMember,filter,groupNameAttribute,description}` |
| POST | `/access/api/v1/ldap/groups/search?username=` | 200 | `[{group_name,group_dn,description,required_update}]`; stores nothing |
| POST | `/access/api/v1/ldap/groups/import` | 204 | `{ldap_group_settings,import_groups}` |
| POST | `/access/api/v1/ldap/groups/{name}/refresh?operation=&username=` | 200 `text/plain` | `UPDATE`, `IMPORT` or `UPDATE_AND_IMPORT` (the default) |

- **The setting.** `{name,enabled_ldap,group_base_dn,group_name_attribute,
  group_member_attribute,sub_tree,force_attribute_search,filter,description_attribute,
  strategy}`, with `LdapGroupSetting`'s defaults: `group_name_attribute` `cn`, `strategy`
  `STATIC`, the rest empty or false. The strategies are `STATIC`, `DYNAMIC` and
  `HIERARCHICAL` (`LdapGroupPopulatorStrategies`, both providers' validators). The
  descriptor's `security.ldapGroupSettings` writes the same settings
  ([research 06](06-system-configuration-and-descriptor.md#security)).
- **Strategies.** The LDAP page: STATIC groups list their members' DNs or IDs in the
  member attribute; DYNAMIC users list their groups' DNs or names (`memberOf`); with
  HIERARCHICAL the user's DN names the groups, "uid=user1,ou=developers,ou=uk,dc=jfrog,dc=org
  indicates that user1 belongs to two groups: uk and developers". The strategy defaults
  are `GroupMappingStrategyService`'s, which answers STATIC's for a value it does not know;
  the UI sends `hierarchy`.
- **Statuses.** `RefreshLdapGroupService`: no local group of the name is `DOES_NOT_EXIST`;
  one whose `realm_attributes` carry another `groupDn=` (or none) is `REQUIRES_UPDATE`;
  the same DN is `IN_ARTIFACTORY`.
- **Imported groups.** Realm `ldap`, the name in lower case ("LDAP groups ... are
  imported in lower case", create a group), `realm_attributes`
  `ldapGroupName=<name>;groupsStrategy=<STRATEGY>;groupDn=<dn>` (the LDAP page's REST
  example).
- **Memberships.** "Once logged-in, you are automatically associated with your LDAP
  groups"; "Users association to these groups is external and controlled strictly by
  LDAP". Each fresh login of a directory user reads its groups through every group setting
  of the connection that accepted it and makes them its memberships in the `ldap`-realm
  groups (`LdapUtils.populateGroups`); a user the directory no longer finds loses them
  (`security.ldap.group.policy.cleanOnFail`, on by default). Permissions are read at
  request time, so a token issued before loses a revoked group's authority at once.
- **One group by name.** Create a Group's `import_from_ldap_group_setting` names the
  setting and `name` the directory group, which the directory matches "with its own
  comparison rule for the group name attribute" (case-insensitive for `cn`); the group is
  created in the `ldap` realm under the directory's name in lower case, which the response
  carries, and "LDAP login keeps its members in sync". "The name must identify a single
  group." Replace or Create a Group accepts the field for an existing group only "when the
  group was imported from the same LDAP group", keeps its members, and answers 400
  otherwise; a missing setting or directory group answers 404 (the
  [creategroup](https://docs.jfrog.com/administration/reference/creategroup) and
  [replacegroup](https://docs.jfrog.com/administration/reference/replacegroup) pages).

Local rules, each **[I]** and listed on [#216](https://github.com/weaveplatform/artifactweave/issues/216):

- Validation: the name is not empty and has no `/` or control character; the strategy is
  one of the three; `group_base_dn` is a DN; the attributes are attribute names, the
  member attribute optionally with an extensible match rule
  (`member:1.2.840.113556.1.4.1941:`, the Entra ID page's nested-group form, passed to the
  directory as written); the filter's parentheses balance. A filter without parentheses is
  wrapped, and an empty one selects every entry.
- A second `POST` of a name answers 400 and `PUT` replaces from the defaults, as for
  connections. A search or refresh whose `enabled_ldap` names no enabled connection, or a
  search for a user no connection finds ("Could not find DN for user"), answers 400, as
  does a directory failure (`LDAP group search failed: <step>: <reason>`).
- `force_attribute_search` is stored and returned; artifactweave always names the
  attributes it reads, so it changes nothing.
- No strategy follows one group to another: STATIC matches the user's own DN or name,
  DYNAMIC resolves each value the user lists, HIERARCHICAL reads the DN. Nested groups are
  the directory's (Entra ID's `msds-memberOfTransitive` or the match rule above), and a
  nesting loop costs nothing. One search returns at most 10000 groups, and a DYNAMIC user
  is read for at most 1000 values.
- A login's memberships change only in groups of the `ldap` realm; memberships an
  administrator gave in other realms stay. A login whose groups cannot be read still
  succeeds and leaves its memberships as they were; the next fresh login reads them
  again. A remembered login (see [LDAP login](#ldap-login)) reads no groups. Users that
  are not stored (`auto_create_user` false) act in the auto-join groups only.
- An import is all or nothing and checks each status: `DOES_NOT_EXIST` creates and is
  refused if the name was taken since, `REQUIRES_UPDATE` takes over the local group of the
  name (keeping its members), `IN_ARTIFACTORY` is skipped. A refresh imports only names no
  local group has and never takes over a group of another realm.
- The refresh asks the directory outside any transaction, then one guarded transaction
  rechecks the connections' and group settings' revisions and publishes every group and
  membership together; a setting changed or deleted meanwhile answers 400 or 404 and
  publishes nothing. Its answer is `Imported <n> LDAP groups, updated <n> and changed the
  memberships of <n> users`; JFrog documents only that it carries the number imported.
- Create a Group's import asks the directory the same way and stores the group only while
  the settings it was found through are unchanged. A name that finds two groups, or one
  no local group can take, answers 400. The group takes the directory's description
  unless the request sends one, as an import from the LDAP Groups screen does. A HIERARCHICAL
  unit without the name attribute is found by its RDN, as a refresh names it.

### Internal password login

Sources: [supported Access configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations)
(`security.authentication.basic-authentication-enabled`, "enable basic authentication
using internal passwords, for users whose accounts are managed directly in the JFrog
Platform", and `password-encryption`), [LDAP](https://docs.jfrog.com/administration/docs/ldap)
("Enabling LDAP disables internal password authentication by default"), the 7.104.0 and
7.104.5 self-managed release notes ("when using all external authentication methods, for
example, SAML or LDAP, basic authentication using internal passwords will be disabled by
default"; "Enabling single sign-on authentication now disables internal password
authentication by default"), and **[J]**
[JA-20120](https://jfrog.atlassian.net/rest/api/2/issue/JA-20120) on 7.161 for the UI's
exclusion list. Implemented by CFG-04
([#1828](https://github.com/weaveplatform/artifactweave/issues/1828)).

- **Surface.** The UI checkbox "Disable Internal Password Login" is the Access setting
  above; its REST surface is
  `GET/PUT /access/api/v1/config/security/authentication/basic_authentication_enabled`.
  Neither the YAML descriptor page nor the 7.161.26 `artifactory.xsd` carries it, so the
  descriptor does not either.
- **What it turns off.** Internal passwords on every Basic login, and the encrypted
  passwords standing for them **[I]**: the encrypted password is the internal password in
  another form, so a switch that turns internal passwords off is read as turning it off
  too; before CFG-04 artifactweave accepted it with the switch off. A directory's password goes on logging in through an
  enabled LDAP connection, a local user's included, as do access tokens. With no
  connection enabled no password can verify, and the answer is the 401 the switch gave
  before LDAP existed: "Basic authentication with a password is disabled; use an access
  token".
- **The default.** Unset means on. Writing the first enabled connection, through the
  Access API or the descriptor, writes it off in the same transaction while it was never
  written; a value an administrator wrote is never overwritten, and disabling or deleting
  connections never writes it back on **[I]**: JFrog documents the default, not when it
  is applied, nor what an install upgraded with LDAP already enabled sees.
- **No exemption.** 7.x exempts only the users of the UI's "Internal users allowed to use
  basic authentication" list, which has no documented REST or configuration surface;
  artifactweave has no UI and exempts no one, the local administrator included. This
  conflicts with decision [0019](decisions/0019-human-authentication-tiers.md)'s local
  administrator fallback, which that decision now records: the fallback is an
  administrator access token, or the switch written on before or after the connection is
  enabled.

### Password encryption policy

The Access `password-encryption` setting (`supported`, `required`, `unsupported`), the
descriptor's `security.passwordSettings.encryptionPolicy`
(`REQUIRED|SUPPORTED|UNSUPPORTED` in the YAML page and `artifactory_general_security`,
lower case in the descriptor classes 7.161.26 still ships). Implemented by CFG-04.

- `supported`, the default, accepts the clear-text and the encrypted password.
- `required` refuses a clear-text password with 401 "Only encrypted passwords are
  accepted; send the encrypted password instead of the clear-text password" **[I]**
  (JFrog documents only that clear-text authentication fails), before the user is read,
  so nothing is counted. A UI request (`/ui/` or `Request-Agent: artifactoryUI`) is
  exempt **[I]**: the encrypted password can only be fetched with the clear-text one.
  Access tokens are unaffected.
- `unsupported` refuses the encrypted password, as a Basic password, in
  `X-JFrog-Art-Api` and in the rechecks of work that outlives the request.
- `GET /artifactory/api/securityconfig` reports the policy in upper case with
  `encryptionRequired` and `encryptionEnabled`
  ([research 06](06-system-configuration-and-descriptor.md#general-security-read)).

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
   7.x also reads the header's older name, `X-Api-Key`, when `X-JFrog-Art-Api` is blank,
   whenever it is sent, even empty (`AuthenticationFilterUtils.getApiKeyTokenKeyValue`,
   7.161.26 open source), and refuses an access token sent in either
   (`AccessTokenAuthenticationProviderV2.assertNotTokenWithApiKeyHeader`). The props filter
   that reads them comes before the Basic filter (`AuthenticationFilterListSorter`) and
   accepts every request that sends a key (`PropsAuthenticationFilter.acceptFilter`), so the
   key alone authenticates such a request, whatever its `Authorization` header says. A
   refused key answers 403 while either header is not blank: "Return 403 to any API client
   that used the API Key header ... If not the client keeps asking for credentials
   indefinitely on 401 responses"
   (`ArtifactoryBasicAuthenticationEntryPoint.isAlreadyAuthedApiKeyRequest`), with the Basic
   challenge the entry point adds to every answer that is not for the UI. Its message is the
   provider's: Access's providers answer "Bad Credentials" when Access refuses it
   (`AccessAuthProviderUtils`), but which provider reads a key is not open source, so
   artifactweave's "Bad Credentials" is **[I]**, and
   an `X-Api-Key` sent empty, read as a key but not one that turns the refusal into 403,
   401 **[I]** ([#1464](https://github.com/weaveplatform/artifactweave/issues/1464)). The
   same entry point answers 403 "if bad credentials error" to a client whose `User-Agent`
   contains `nuget` in any case and that sent an `Authorization` or `X-NuGet-ApiKey` value
   that is not blank (`isAlreadyAuthedNuGetRequest`), which artifactweave follows on every
   route (decision [0038](decisions/0038-nuget.md),
   [#1465](https://github.com/weaveplatform/artifactweave/issues/1465)).
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
5. Internal password login can be turned off platform-wide through
   `/access/api/v1/config/security/authentication/basic_authentication_enabled`
   ([internal password login](#internal-password-login)).
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
   7.31.10 to 7.55.6). artifactweave answers it 403 "Login has failed. Due to Incorrect
   username/password or locked user." before the suspension and the password, and counts
   nothing ([#475](https://github.com/weaveplatform/artifactweave/issues/475)). The status and
   message are 7.161.26's: `AccessFilter`'s `SecurityUtils.ensureLoginAttemptAllowed` throws
   `UserLockedException.userLocked()` with that message before any authentication filter runs,
   and `ArtifactoryBasicAuthenticationEntryPoint` answers the `LockedException` 403. The
   message names a wrong password as well, by design, "to avoid any clue for the hacker to
   identify valid user accounts" (**[J]**
   [RTFACT-24156](https://jfrog.atlassian.net/browse/RTFACT-24156), 7.11); OSS 6.20's "User is
   Locked.\nContact System Administrator to Unlock The Account." is not 7.x's.
   The entry point's `sendErrorResponse` adds `WWW-Authenticate: Basic realm="Artifactory
   Realm"` to the 403 of a locked account, an expired password and a suspended login, as to
   every answer it gives but its UI's (`UiRequestUtils.isUiRestRequest`: a path under
   `/artifactory/ui/` or `Request-Agent: artifactoryUI`), and artifactweave sends it with each
   on every route, the authenticated user's 403 and the API restriction's excepted
   ([#1485](https://github.com/weaveplatform/artifactweave/issues/1485)).
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
8. **Authorization values that carry no credential.** 7.x reads Basic credentials only from
   an `Authorization` value that starts with exactly `Basic ` and goes on with a value neither
   blank nor `Og==`, the encoding of an empty user name and password
   (`RequestUtils.getBasicAuthorization`, 7.161.26 open source;
   `ArtifactoryBasicAuthenticationFilter.acceptFilter` accepts a request only then). It reads
   a Bearer token in two places: the props filter takes `Bearer ` exactly in a value of nine
   characters or more (`AuthenticationFilterUtils.getOauthTokenKeyValue`, with
   `old.check.for.oath.token.length` at its default), and
   `RequestUtils.getBearerAuthenticationValue` takes `bearer ` in any case followed by a value
   that is not blank. No filter of the open source chain reads another scheme, and the props
   filter skips a blank `X-JFrog-Art-Api` for `X-Api-Key` (`getApiKeyTokenKeyValue`). The Basic filter also declines the anonymous user name, in any case, while
   anonymous access is off (`isAnonAccessEnabled() || !UserInfo.isAnonymous(username)`). So
   `AccessFilter.useAnonymousIfPossible` serves `Basic Og==`, a blank `Basic`, the Basic scheme
   in another case (`basic`, `BASIC`) or followed by a tab, `Bearer` with no token, another
   scheme (`Negotiate`, `Token`, `Digest`) or a bare value, a blank `X-JFrog-Art-Api` without
   `X-Api-Key`, and,
   while anonymous access is off, Basic credentials naming `anonymous` as a request without
   credentials: anonymous while
   anonymous access is on, 401 "Authentication is required" while it is off, and a ping
   either way (`AnonymousPingInterceptor`). JFrog settled it for `Og==` in 3.4.0: "Artifactory
   should ignore it and continue as anonymous", which "is a must when using Docker as
   anonymous without ~/.dockercfg file" (**[J]**
   [RTFACT-6765](https://jfrog.atlassian.net/browse/RTFACT-6765)). artifactweave does the
   same on every route and counts no failed login for these values
   ([#1451](https://github.com/weaveplatform/artifactweave/issues/1451)); Cargo's and
   RubyGems' bare tokens are still read on their own paths. A credential in another header
   still counts: the props filter reads an API key before and whatever `Authorization` says
   (item 3, `AuthenticationFilterUtils.getTokenKeyValue`), and the NuGet routes read `X-NuGet-ApiKey`
   when no `Authorization` value counts **[I]**. The Basic filter stands aside on
   `/api/docker/`, `/api/oci/`, `/api/nuget/` and `/api/vagrant/`
   (`RequestUtils.pkgEndpointMatchBasicAuth`), where filters outside the open source read
   Basic, and the filter that verifies a Bearer token is not open source either; that they
   treat these values alike is **[I]**, as is any commercial filter that might read them
   elsewhere. The Basic filter declines a password that fails the password policy too, but
   only while Access's `security.authentication.verify-password-policy` is on, which the
   supported Access configurations page documents as `false` by default; artifactweave has
   no such setting and never declines on the policy.

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
https://docs.jfrog.com/administration/reference/jfrog-platform-groups-api/llms.txt,
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

**Settled by [#1485](https://github.com/weaveplatform/artifactweave/issues/1485).** A locked
account, an expired password and a suspended login answer 403 with the entry point's Basic
challenge, which no answer to a request of the UI's carries, and a locked account's message is
7.161.26's "Login has failed. Due to Incorrect username/password or locked user." rather than
OSS 6.20's (item 5 of [Authentication](#authentication-accepted-on-every-endpoint)).

**Settled by [#1486](https://github.com/weaveplatform/artifactweave/issues/1486).** Credentials
that do not verify answer 401 "Bad Credentials", with a capital C, where artifactweave answered
"Bad credentials". With the internal realm enabled in Access, a wrong password reaches
`AccessInternalAuthenticationProvider`, the last provider in `security.xml` since 7.55, and
Access's refusal ends in `AccessAuthProviderUtils.handleAccessClientHttpException`, which
throws `BadCredentialsException("Bad Credentials")` from 7.117.5 on ("Bad credentials" in
7.55.2 to 7.111.22). `DbAuthenticationProvider` and Spring's `DaoAuthenticationProvider`
still say "Bad credentials", but 7.x tries them only while Access does not handle the internal
realm. Access 7.191.16, shipped with 7.161.26, lists `internal` first in
`security.authentication.enabled-realms` in its shipped `example.config.internal.yml`, so the
realm is on unless an administrator turns it off. Captures agree: `GET /v2/_catalog` with a
wrong password answered `{"errors":[{"status":401,"message":"Bad Credentials"}]}` on 7.117.12
(**[J]** [RTFACT-31253](https://jfrog.atlassian.net/browse/RTFACT-31253)), JFrog's own
package-reroute README says Docker Hub credentials sent to the token endpoint "produce
`Bad Credentials`", and a default `jfrog/artifactory-jcr` install (7.146.17) answers invalid
credentials 401 "Bad Credentials" (stackrox/stackrox PR 21328). Every lower-case capture comes
from before 7.117 (RTFACT-26849 on 7.31.10, jfrog-cli issue 1616 on 7.41.6, RTFACT-29440 on
SaaS 7.53) or names no version. The same text answers a token or API key that does not verify,
which 7.x answers through Access's providers as well; what 7.x says for each is **[I]**:
`AccessTokenAuthenticationProvider` answers "Token failed verification: " and Access's reason,
a reason no source maps to the fault (a valid access token under another user name gave
"signature" on SaaS 7.53.1, RTFACT-29440), and an API key header answers the provider's message.
The Access routes answer the same text, which is **[I]** as well: Access words its own
refusals, and the one capture, with no version, shows Basic credentials from `~/.netrc` on an
OIDC identity mappings route answered 401
`{"errors":[{"code":"UNAUTHORIZED","message":"HTTP 401 Unauthorized","detail":"Request has failed. Due to incorrect username/password or locked user."}]}`
(**[KB]** [Access OIDC 401 errors caused by .netrc overrides](https://jfrog.com/help/r/artifactory-how-to-fix-access-oidc-401-unauthorized-errors-caused-by-netrc-overrides)).

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
- The answer of the Access APIs to a correct password that has expired, and to a locked
  account. **[I]** The same 403 and message as Artifactory's routes, with its Basic challenge.
  On Artifactory's routes 7.161.26 settles both: `AccessAuthProviderUtils` raises
  `CredentialsExpiredException("Password has expired")` when Access reports
  `PASSWORD_EXPIRED` (**[J]** [RTFACT-17846](https://jfrog.atlassian.net/browse/RTFACT-17846)
  logs it), and `ArtifactoryBasicAuthenticationEntryPoint` answers it 403 with the challenge,
  as it does a locked account; Access answers its own APIs and is not open source.
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
- Whether the commercial filters read `Basic Og==`, a blank `Basic`, a Basic scheme spelled
  other than `Basic `, a blank `Bearer`, another scheme or a bare value. **[I]** None does:
  the open source chain serves them as anonymous (item 8 of
  [Authentication](#authentication-accepted-on-every-endpoint)), on the Docker, OCI, NuGet
  and Vagrant paths too, and `X-NuGet-ApiKey` is read beside them.
- What Access answers Basic credentials naming `anonymous` while anonymous access is on,
  which the Basic filter reads and passes to Access. **[I]** 401 "Bad Credentials", counted
  as a failed login: the anonymous user has no password.
- The legacy delete of the built-in `admin` or `anonymous` user. **[I]** 400 with
  `the built-in user "admin" cannot be deleted`, the message of Access v2's 403. Access, which refuses
  it in 7.x, is closed; the UI's `DeleteUserService` answers 403 "Action cancelled. " followed
  by Access's message; OSS 6.20's `SecurityServiceImpl.deleteUser` refuses nothing, and
  6.11.1 deleted `admin` (**[J]** [RTFACT-19575](https://jfrog.atlassian.net/browse/RTFACT-19575))
  ([#1449](https://github.com/weaveplatform/artifactweave/issues/1449)).
