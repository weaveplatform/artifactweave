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
a repository's `environments` field is validated against them.

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
