# 0013: Projects and environments

Status: Proposed

## Context

JFrog projects group repositories, members and roles under a key that prefixes resource
names, with storage quotas and project-scoped admin privileges. Environments (`DEV` and
`PROD` built in, plus custom ones) tag repositories and are mandatory when a repository is
assigned to a project. The `jfrog/project` Terraform provider manages all of it and polls
repository configuration after an attach until it reflects the project.

## Decision

Projects, roles, membership, repository attach and share, and environments are implemented
per [07-webhooks-retention-projects](../07-webhooks-retention-projects.md): key
constraints (`^[a-z][a-z0-9-]{1,31}$`, immutable), `admin_privileges`, `soft_limit`,
`storage_quota_bytes` and `storage_quota_email_notification`, the predefined roles,
custom roles with the project action vocabulary, and the `<project_key>-` prefix rule for
repositories created inside a project. Attaching a repository updates its configuration
synchronously so a following read shows `projectKey`. Each project gets its
`<project_key>-build-info` repository. Storage usage is accounted per project and the
quota warns and blocks at the documented thresholds.

Global and project environments live in the Access API with the documented endpoints;
a repository's `environments` field is validated against them. Every write path checks
it: create, update, the batch endpoints and the descriptor. An unknown name answers 400
"Environment 'X' does not exist" and writes nothing. Accepted names are stored in the
environment's own spelling, so rename and delete find every repository that uses it.

**Global roles and environments** (`/access/api/v1/roles`, `/access/api/v1/environments`,
issue #77, `internal/service/roles`):
- **Built-ins:** their defaults are defined in code (`types.BuiltinGlobalRoles`,
  `types.BuiltinEnvironments`).
  - The roles are "Project Admin" (`ADMIN`, the whole global vocabulary on DEV and PROD)
    and Developer, Contributor, Viewer, Release Manager and Security Manager
    (`PREDEFINED`). Application Admin and the ML roles belong to products artifactweave
    does not serve.
  - **Editable predefined roles** (issue #100), as JFrog documents
    (`manage-global-roles`, `assign-environments-to-global-and-project-roles`):
    - PUT may change a `PREDEFINED` role's actions and environments. The change is
      stored as a role row of the same name and laid over the defaults; the built-in
      description stays.
    - Renaming, deleting or changing the type of any built-in role answers 400, as does
      any change to Project Admin.
  - **Default action sets:**
    - Developer's default is JFrog's one published example (`manage-project-roles`):
      Read and Write in DEV and PROD, expanded through the Basic actions' documented
      bundles.
    - JFrog documents the other predefined roles' defaults only in its UI, so those are
      chosen to match each role's documented purpose.
  - DEV and PROD can't be renamed or deleted.
- **Create-role response:** `POST /access/api/v1/roles` answers 201 with
  `{"code":"OK","message":"Role '<name>' created"}`, as JFrog's spec documents
  (`createglobalrole.json`). Our spec had drifted to "no body" and is corrected.
- **Names:** environment names are compared without regard to case, across global and
  project environments.
- **Rename** updates roles, project assignments and repository configurations in one
  transaction.
- **Delete** refuses, with 400, DEV, PROD, and any environment a repository or global role
  still names.
  - **To verify against a live Artifactory:** JFrog's spec lists only 204, 401, 403 and
    404 for this operation, so both 400 cases are artifactweave's choices: refusing
    built-ins, and refusing an environment still in use rather than detaching it.
- **Custom global roles:**
  - At most 30, as JFrog allows. Stored changes to predefined roles don't count.
  - Actions are checked against the 25-value global vocabulary, and environments must
    exist.
  - Deleting a role removes it from every project membership; a membership left with no
    role is removed.
- **Reading a role for the permission evaluator:** `roles.Service.Role`, then
  `types.RoleActions`.

## Rationale

Projects are how multi-team Artifactory instances are organised and what the Terraform
modules in those organisations already declare; without them the `jfrog/project`
provider is unusable and the `projectKey` fields on repositories mean nothing.

**Alternatives considered:**
- *Flat tenancy with naming conventions only*: no quotas, no project roles, provider
  incompatible.

## Constraints

- Project-scoped tokens and roles are evaluated by the permission model
  ([0006](0006-authorization-native-permission-model.md)).
- Project admins may create and delete repositories only within their granular privileges.

## Verification

- Key validation and prefix tests.
- Attach, share and unshare tests including the synchronous `projectKey` read.
- `jfrog/project` provider resources in the Terraform acceptance fixture.

## References

- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), projects section
- <https://docs.jfrog.com/projects/reference/llms.txt>
