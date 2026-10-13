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

## Verification

- A route × principal matrix test over every operation with admin, a scoped user, a group
  member, a project member, a narrowed token and anonymous.
- Converter round-trip tests across v1, v2 and Access v2.
- `jf rt permission-target-*`, `artifactory_permission_target` and `platform_permission`
  in the acceptance suite.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md),
  permission shapes and action vocabularies
- <https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2>
- <https://github.com/jfrog/terraform-provider-platform/blob/main/docs/resources/permission.md>
