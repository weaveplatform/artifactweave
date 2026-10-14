# 0006: Authorization: native Artifactory permission model

Status: Proposed

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
  scope, the subject acts as an administrator only when the scope keeps applied permissions:
  `applied-permissions/user` or `admin` for its own flag, or a groups scope naming an admin
  group.
- **Token scopes** only remove; the evaluator never adds what the subject does not hold:
  - `applied-permissions/user` and `applied-permissions/admin` keep the user's own and
    group grants.
  - `applied-permissions/groups:…` keeps only the named groups the subject belongs to, and
    drops the user's direct grants.
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
  - The build section takes exactly one target, `artifactory-build-info`.
  - Named users and groups exist.
- **What a write replaces:**
  - A v1 PUT replaces only the artifact section.
  - A v2 PUT replaces artifact, build and release_bundle whole, and keeps the description,
    destination and pipeline_source, which v2 cannot carry.
  - Access stores and echoes destination and pipeline_source. The generated types drop
    them, so create reads the raw body.
  - An Access PATCH on a section merges users, groups and targets by key; a PUT replaces it.
- **Who may** (documented, each page's Security note):
  - v1, and every Access write: an administrator.
  - Access reads and listing: an administrator or a `system:permissions:r` token
    (`getpermissiondetails.json`, `getpermissions.json`).
  - v2 reads, HEAD, listings and the by-user and by-group views: an administrator, or a
    holder of MANAGE, who sees the permissions it manages.
  - v2 PUT: an administrator, or a MANAGE holder who "can only edit the actions"
    (`createorreplacepermissiontargetv2.json`). A replace that changes a section, target
    or pattern answers that holder 403.
  - v2 POST and DELETE: an administrator (`createpermissiontargetv2.json`,
    `deletepermissiontargetv2.json`).
  - The request's evaluator decides MANAGE (`Decider.Holds`) under the caller's token
    scope, so a groups-scoped token loses a user's direct grant.
- **A missing name answers a non-administrator 403**, as one it may not see. *Inferred:*
  details need an administrator, MANAGE on the target or a `system:permissions:r` token, and
  no one holds MANAGE on a target that does not exist. Answering 404 would let anyone probe
  for names.
- **Different patterns per target** (*undocumented*): v1 and v2 hold one pattern list per
  section, so such a permission has no legacy view.
  - The v1 and v2 listings still show it by name and URI, so it is not invisible.
  - Details answer 400 naming `/access/api/v2/permissions`, because a merged view would
    grant every pattern on every target.
  - The v2 by-user and by-group views carry full documents and leave it out.
- **Access listing** (documented, `getpermissions.json`):
  - `expand` without a `user` or `group` filter answers 400.
  - A filter naming a user or group that does not exist answers 404.
  - `limit` runs from 1 to 99999.
  - The cursor is the name of the page's last permission, passed back as the next call's
    cursor; the next page starts after it. It is absent when no more remain.
  - `expand` returns full documents narrowed to the requested principals' grants. With
    `include_inherited_groups` (the default), the named users' groups count among them
    (*inferred*: their grants are why the permission matched).
  - Not done: the page matches user names case-insensitively; the user store is
    case-sensitive.
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
