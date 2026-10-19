# 0006: Authorization: native Artifactory permission model

Status: Proposed

Checked against: Artifactory 7.161.32 (self-managed; SaaS 7.174.0); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#artifactory-7x-check) records it.

## Context

Artifactory authorizes through an admin flag, permission targets that grant actions on
repositories (and builds and release bundles) to users and groups with include and
exclude patterns, groups with auto-join, project roles, and token scopes that narrow what
a token may do. Three wire formats express the same model: the legacy v1 target
(`r n w d m mxm x` codes), the v2 target under `/api/v2/security/permissions`
(`read annotate write delete manage managedXrayMeta distribute`) and the Access v2
permission (`READ ANNOTATE WRITE DELETE SCAN MANAGE EXECUTE` with per-resource targets).
hostweave used Cedar for its own policy model.

## Decision

A native evaluator in `internal/authz`, not Cedar. One internal representation shaped
like the Access v2 permission (resources, actions per user and group, targets with
include and exclude patterns) with converters to and from the v1 and v2 legacy formats.

Evaluation order for a request: admin flag; then the union of permission targets that
name the principal directly or through group membership, filtered by target (`ANY`,
`ANY LOCAL`, `ANY REMOTE`, `ANY DISTRIBUTION` or a repository key; remote caches are
covered by their remote repository's grants) and by Ant-style include and exclude
patterns against the item path; then project roles for project-scoped resources; then
token scopes, which can only remove actions. `anonymous` is an ordinary principal whose
grants only apply when anonymous access is enabled.

Action mapping between formats is a fixed table in the research. Repository listing and
configuration reads honour the `repo:<key>:r` and `system:info:r` scopes as Artifactory
does; non-admin readers receive the partial configuration.

## Rationale

The semantics are dictated by three wire formats that clients round-trip, so the model
has to be Artifactory's. A policy language on top would add a translation layer without
adding expressiveness the clients can use. One internal shape with converters keeps the
evaluator small and makes the legacy shims thin.

**Alternatives considered:**
- *Cedar as in hostweave*: good for a policy model we own; here every policy would be a
  generated translation of a permission target.
- *Implementing only Access v2*: breaks the JFrog CLI's `permission-target-*` commands and
  the `artifactory_permission_target` resource.

## Artifactory 7.x check

The decision stands against 7.161.32 (SaaS 7.174.0), audited on 2026-10-18 with the KB, Jira
and client sources ([#378](https://github.com/weaveplatform/artifactweave/issues/378);
[research 05](../05-platform-identity-and-security-apis.md#artifactory-7x-check)). The
evaluator's basics match 7.x: grants are additive, a virtual repository has no permission
target of its own, and anonymous gets 401 where a user gets 403
([permissions](https://docs.jfrog.com/administration/docs/permissions),
[security configuration](https://docs.jfrog.com/administration/docs/security-configuration)).
So do the routes, statuses and shapes of the v1, v2 and Access v2 permission APIs. Reading
one Access permission section needs an administrator, as in 7.x: "Requires admin privileges"
([getpermissionresource](https://docs.jfrog.com/administration/reference/getpermissionresource)).
A `system:permissions:r` token, which lists permissions and reads one whole, is refused there
([#382](https://github.com/weaveplatform/artifactweave/issues/382)). So does what a MANAGE
holder may grant: one who is not an administrator grants only the actions it holds, and never
MANAGE. This is `exclude-manage`, the Permissions V2 default, under which "Users with manage
permissions can grant only permissions that they have, except for manage permissions", and
"Users with the manage permission cannot grant or revoke the manage action"
([permissions](https://docs.jfrog.com/administration/docs/permissions)). Nor may such a holder
change its own entry: the same page lets it "assign and modify permissions granted to other
users and groups". The v2 PUT is the only
write such a holder reaches; the v1 and Access writes need an administrator. So does the admin
scope: `applied-permissions/admin` makes the token an admin token whatever its subject, a user
who is no administrator or one that does not exist
([create scoped token](https://docs.jfrog.com/administration/reference/create-scoped-token):
"the token will be granted admin permissions"), and only an administrator may mint it
([#406](https://github.com/weaveplatform/artifactweave/issues/406)). These match 7.x since
[#408](https://github.com/weaveplatform/artifactweave/issues/408):
- The Access listing's `user` filter matches user names without regard to case, and the
  `group` filter group names exactly
  ([getpermissions](https://docs.jfrog.com/administration/reference/getpermissions): "Usernames
  are matched case-insensitively, group names exactly").
- The Manage Resources role, a user's `resources_manager` or a group's `manage_resources`, lets
  its holders "create, edit, and delete permissions on any resource type"
  ([permissions](https://docs.jfrog.com/administration/docs/permissions)) and see every
  permission target (JA-16022 in
  [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)).
  [Manage Resources](#the-manage-resources-role-408) records what it reaches.
- A v2 `build` section without `repositories` targets `artifactory-build-info`, which "cannot
  be changed", and a `releaseBundle` section `release-bundles`
  ([createorreplacepermissiontargetv2](https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2)).

[Research 05](../05-platform-identity-and-security-apis.md#artifactory-7x-check) records the
`+` that stands for a space in permission names. The seeded permissions match 7.x, as
[Seeded permissions](#seeded-permissions-383) records.

## Constraints

- Pattern matching follows Ant semantics (`*`, `**`, `?`), case-sensitive, and is tested
  against the documented examples.
- A permission target that names a repository that no longer exists is kept and ignored,
  as Artifactory does.
- Build and release-bundle resources are accepted in targets; only the build resource has
  behaviour in v1.

## Implementation notes (`internal/authz`, #75)

These are choices made where no source documents Artifactory's behaviour:
- **Patterns:**
  - An empty include list means `**`, Artifactory's default; an empty exclude list excludes
    nothing.
  - A trailing `/` means everything below, as in Ant.
  - `dir/**` also matches `dir` itself.
- **Folders:** a folder is visible when an include pattern matches it or could match a path
  below it (Ant's `matchPatternStart`), unless an exclude pattern matches the folder. A
  listing therefore shows the folders that lead to permitted content.
- **Remote caches:** `<remote>-cache` resolves to the remote for target matching, so
  `ANY REMOTE` and the remote's own key cover its cache.
- **Admin:** the subject's own flag or any of its groups' admin privileges. Under a token
  scope, `applied-permissions/admin` is an administrator whatever its subject. Otherwise the
  subject acts as an administrator only when the scope keeps applied permissions:
  `applied-permissions/user` for its own flag, or a groups scope naming an admin group.
- **Token scopes** only remove, apart from the groups a groups scope names and
  `applied-permissions/admin`; the evaluator never adds what the subject does not hold
  otherwise:
  - `applied-permissions/user` and `applied-permissions/admin` keep the user's own and
    group grants.
  - `applied-permissions/groups:…` acts through every group it names, whether or not the
    subject belongs to it, and drops the user's direct grants. The evaluator keeps only the
    named groups the subject has, so `auth.Principal.Subject` first adds the named groups to
    the subject (`internal/auth/principal.go`). Access documents the same: a groups-only
    token acts through "Explicitly mapped groups only"
    ([access tokens](https://docs.jfrog.com/administration/docs/access-tokens), "Transient
    Users and Auto Join Groups").
  - `applied-permissions/roles:…` grants nothing until projects arrive (P11).
  - `artifact:<repo|*>[/<ant path>]:<letters>` entries intersect artifact access with what
    they name.
  - A scope of resource entries only reaches artifacts through its artifact entries, and
    no other resource.
  - An administrator's resource-only token keeps every action within those entries, but
    loses the admin-only endpoints.
- **`repo:<key>:r`** narrows configuration reads to the named repositories. **`system:<area>`**
  scopes are the only way a non-admin reaches system capabilities.

## Applying it to requests (#80)

- **Once per request.** The authentication middleware authorises every principal (`auth.Authorize`): it builds the `authz.Decider` from the user's own admin flag, groups and token scope, and sets the principal's `Admin` and `Groups` to the evaluator's answers. A token scope can remove the admin flag, an admin-privilege group adds it, and `/groups` acts through the named groups. Handlers ask that one decider every question, and nothing reads the permissions again.
- **Paths.**
  - Deploy, download, delete, properties, copy, move and multipart check the item's repository-relative path.
  - A download or storage read first checks the requested path as a folder, so it allows anything at or below it. Once the item is found, which in a virtual repository means in a member, the exact right is checked.
  - Listings, folder archives and AQL rows leave out what the caller may not read. A folder shows when something below it is readable.
- **Repository visibility** (listings and configuration reads):
  - an administrator sees every repository;
  - a token scope with `repo:` entries sees exactly those repositories;
  - anyone else sees the repositories they may read anywhere in, and every repository under `system:info:r`.
- **The `?permissions` storage view** reports grants whose target patterns admit the item's path.
- **Known approximations:**
  - Explode-on-deploy checks the right to overwrite at the archive's path, not at each entry.
  - A recursive property edit checks annotate at the folder, not at each item below it.

## Implementation notes (permission APIs, #82)

`internal/service/permission` validates and stores; the v1, v2 and Access v2 handlers in
`internal/server/permissions.go` are views over it, so a permission written in one format
reads back in the others. Sources are the JFrog OpenAPI pages the parity oracle caches,
`.specoracle/<family>/<page>.json`. Each rule is marked documented, inferred or
undocumented.

- **Validation**, shared by every API, answers 400:
  - Names are non-empty, without surrounding spaces or a slash, and at most 64 characters.
  - Patterns are at most 1024 characters.
  - Artifact targets are existing local or remote repositories, `<remote>-cache`, or `ANY`,
    `ANY LOCAL`, `ANY REMOTE` and `ANY DISTRIBUTION`. A virtual repository is refused.
  - The build section takes exactly one target, `artifactory-build-info`. A v2 build section
    without `repositories` targets it, and a v2 release bundle section without them
    `release-bundles` (documented, `createorreplacepermissiontargetv2.json`).
  - Named users and groups exist.
- **What a write replaces:**
  - A v1 PUT replaces only the artifact section.
  - A v2 PUT replaces artifact, build and release_bundle whole, and keeps the description,
    destination and pipeline_source, which v2 cannot carry.
  - Access stores and echoes destination and pipeline_source. The generated types drop
    them, so create reads the raw body.
  - An Access PATCH on a section merges users, groups and targets by key; a PUT replaces it.
- **Who may** (documented, each page's Security note). Wherever an administrator may, so may
  a holder of the Manage Resources role ([below](#the-manage-resources-role-408)):
  - v1, and every Access write: an administrator.
  - Access listing and whole-permission reads: an administrator or a `system:permissions:r`
    token (`getpermissiondetails.json`, `getpermissions.json`).
  - Access reads of one section: an administrator
    ([getpermissionresource](https://docs.jfrog.com/administration/reference/getpermissionresource),
    "Requires admin privileges"; #382).
  - v2 reads, HEAD, listings and the by-user and by-group views: an administrator, or a
    holder of MANAGE, who sees the permissions it manages.
  - v2 PUT: an administrator, or a MANAGE holder who "can only edit the actions"
    (`createorreplacepermissiontargetv2.json`). A replace that changes a section, target
    or pattern answers that holder 403.
  - What that holder may grant (documented, `exclude-manage`, the Permissions V2 default;
    [permissions](https://docs.jfrog.com/administration/docs/permissions)):
    - In each section it may grant only the actions it holds there, and never MANAGE, which
      it can neither grant nor revoke. `authz.Decider.Grantable` answers which actions it may
      change, from the stored grants, so a body cannot first give it what it then passes on.
    - Only the sections where it holds MANAGE: the page gives MANAGE separately for the
      repository, build and release bundle sections.
    - **[I]** It may not revoke an action it does not hold either. The page states the rule
      for grants and for MANAGE only; the narrower reading keeps a holder from taking away
      what it could not give back.
    - Not in its own user entry: the page lets a holder "assign and modify permissions granted
      to other users and groups". Any change to that entry, adding or removing it included,
      is refused, even within the actions it holds; repeating it unchanged is not a change.
      `authz.Decider.Self` names the entry by the caller's user, which a token scope does not
      narrow: a groups-scoped token is still its user's.
    - **[I]** Nor in the entry of a group it belongs to, or one a groups-scoped token acts
      through. The page names only "other users and groups"; a group's grants are its
      members' grants, so a change there would rewrite the holder's own rights.
    - **[I]** Such a change answers 403, the status `createorreplacepermissiontargetv2.json`
      gives a caller without the rights the operation needs.
    - `permission.CheckManagerEdit` runs inside the write's transaction, against the
      permission it replaces, so a concurrent change cannot slip between the check and the
      write.
  - v2 POST and DELETE: an administrator (`createpermissiontargetv2.json`,
    `deletepermissiontargetv2.json`).
  - The request's evaluator decides MANAGE (`Decider.Holds`) under the caller's token
    scope, so a groups-scoped token loses a user's direct grant.
- **A missing name answers 403** to a caller that does not see every permission, as one it
  may not see; an administrator, a Manage Resources holder or a `system:permissions:r` token
  gets 404. *Inferred:* details need one of those or MANAGE on the target, and no one holds
  MANAGE on a target that does not exist. Answering 404 would let anyone probe for names.
- **Different patterns per target** (*undocumented*): v1 and v2 hold one pattern list per
  section, so such a permission has no legacy view.
  - The v1 and v2 listings still show it by name and URI, so it is not invisible.
  - Details answer 400 naming `/access/api/v2/permissions`, because a merged view would
    grant every pattern on every target.
  - The v2 by-user and by-group views carry full documents and leave it out.
- **Access listing** (documented, `getpermissions.json`):
  - The page gives the `user`, `group`, `include_inherited_groups` and `expand` parameters as
    available from Artifactory 7.210.0, a release later than both 7.161.32 and SaaS 7.174.0
    ([getpermissions](https://docs.jfrog.com/administration/reference/getpermissions), read
    2026-10-18). artifactweave serves them ahead of that release.
  - `expand` without a `user` or `group` filter answers 400.
  - A filter naming a user or group that does not exist answers 404.
  - `limit` runs from 1 to 99999.
  - The cursor is the name of the page's last permission, passed back as the next call's
    cursor; the next page starts after it. It is absent when no more remain.
  - `expand` returns full documents narrowed to the requested principals' grants. With
    `include_inherited_groups` (the default), the named users' groups count among them
    (*inferred*: their grants are why the permission matched).
  - User names match without regard to case, group names exactly (documented). The user
    store keeps names as given, so `permission.Service.FoldUsers` reads the users and keeps
    every one whose name differs from the filter only in case; a filter for `dev` matches
    both `dev` and `Dev` when both exist (*inferred*: 7.x stores user names in lower case,
    so it never has two).
- **Statuses:**

  | Request | Answer | Source |
  | --- | --- | --- |
  | v1 PUT | 200, `{"info": "Successfully created\|updated permission target 'x'"}` | documented, `createorreplacepermissiontarget.json` |
  | v1 DELETE | 200, text | documented, `deletepermissiontarget.json` |
  | v2 POST | 201, empty; 409 when the name exists | 201 documented, `createpermissiontargetv2.json`; 409 is what jfrog-client-go reads as "already exists" |
  | v2 PUT | 200, create or replace | documented, `createorreplacepermissiontargetv2.json` |
  | v2 HEAD | 200 or 404 | documented, `permissiontargetexistencecheckv2.json` |
  | v2 DELETE | 200 | documented, `deletepermissiontargetv2.json` |
  | Access POST | 201 with the stored document; 409 when the name exists | statuses documented, `createpermission.json`; the body is our choice |
  | Access DELETE | 204 | documented, `deletepermission.json` |
  | Access section DELETE | 204 | documented, `deletepermissionresource.json` |
  | Access list | 400 and 404 as above | documented, `getpermissions.json` |

## The Manage Resources role (#408)

- **What it is.** A platform role, set on a user (`resources_manager`) or a group
  (`manage_resources`) through the Access users and groups APIs, which only an administrator
  calls. The permissions page describes it as "Manage Resources including create, edit, and
  delete permissions on any resource type including Pipeline resources (Integration, Source,
  and Node Pools)". JA-16022 (7.146.7) fixed a holder in a group that also held MANAGE on one
  permission target seeing only that target, when it was expected "to see all other
  permission targets".
- **What it reaches.** Every route of the three permission APIs, as an administrator: list
  and read every permission (a missing name answers 404), create, replace any section,
  target or pattern (where a MANAGE holder changes only the actions), and delete. It does
  not follow `exclude-manage`, which governs holders of the MANAGE action.
- **What it does not reach.** Every other admin-only route: users, groups, repositories,
  system configuration, tokens of other users. So a holder cannot set its own role, an admin
  flag or a group's admin privileges, and a permission grants no admin rights. The role
  grants no content either; a holder may write itself a permission that grants content, as
  the documented "create, edit, and delete permissions on any resource type" allows.
- **Under a token.** It follows the identities the token acts as, as a group's admin
  privileges do (`authz.Decider.ManagesResources`): the user's flag under
  `applied-permissions/user` or no scope, a group's while a groups scope names the group, and
  nothing under a scope of resource or system entries alone. A transient user has no flag
  of its own.
- **Proof.** The route by principal matrix carries `resmgr` (the user's flag), `gresmgr` (a
  group's) and `tok-resmgr-repo` (a resource-scoped token of `resmgr`). The first two manage
  every permission and the token none, and all three are refused every other admin route.
- **[I]** Pipelines resources do not exist here, and the pages name no limit on the role, so
  it reaches all of the permission APIs, the legacy v1 one included; the pages describe the
  role through the UI only.

## Seeded permissions (#383)

- **What the seed grants.** Since 7.84.3 Artifactory ships `Anything` and `Any Remote`
  without `anonymous` ([permissions](https://docs.jfrog.com/administration/docs/permissions);
  "Changes to Anonymous Access" in the
  [7.84.10 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed)).
  artifactweave seeds `Anything` with read for the `readers` group on `ANY`, and `Any Remote`
  on `ANY REMOTE` with no grants (`internal/auth/seed.go`). With anonymous access on, a request
  without credentials reads only what a permission an administrator writes for `anonymous`
  grants, as JFrog advises.
  - *Inferred:* the pages name no user or group in place of `anonymous`, so `Any Remote`
    grants no one.

The inferred point is on [#216](https://github.com/weaveplatform/artifactweave/issues/216).

## Verification

- A route × principal matrix test over every operation with admin, a scoped user, a group
  member, a project member, a narrowed token and anonymous. `internal/server/matrix_test.go`
  (#84) covers every implemented route with all of these but project members, who arrive in
  P11. Each row states who may make its request from this model, and a guard fails while an
  implemented route has no row.
- Converter round-trip tests across v1, v2 and Access v2.
- `jf rt permission-target-*`, `artifactory_permission_target` and `platform_permission`
  in the acceptance suite.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md),
  permission shapes and action vocabularies
- <https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2>
- <https://github.com/jfrog/terraform-provider-platform/blob/main/docs/resources/permission.md>
