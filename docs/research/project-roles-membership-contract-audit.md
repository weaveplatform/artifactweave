# Project roles and membership: contract audit

Audited **2026-11-19** as a prerequisite of
[#487](https://github.com/weaveplatform/artifactweave/issues/487), against current
first-party documentation and the tagged JFrog project provider **v1.9.9**. This
audit establishes the documented routes, shapes, role vocabulary and client
requirements. Several validation and name-resolution edges remain unresolved;
the existing implementation is not evidence of vendor behavior.

## Evidence and boundary

**[D]** means published JFrog documentation/OpenAPI. **[S]** means repository
requirements/source, including explicitly identified first-party client source.
**[I]** means an unresolved inference or a compatibility choice requiring an
explicit decision. A guide rule, a provider validator and a vendor REST guarantee
are distinguished below. No live vendor response was captured.

The source snapshot is `53a71e0833eb577d84ee3b49b6dabcad4a58e19e`, the PR #1730
foundation, on `feat/project-role-services`. Current raw `.md` definitions were
read directly with the documented spec-oracle user agent. Role CRUD definitions
report `updatedAt: 2026-04-05`; role/group lists report `2026-08-10`, and the user
list `2026-08-12`. No applicable `AGENTS.md` was found; the worktree's
[engineering conventions](../../CLAUDE.md) and
[research policy](README.md#target-version-and-sources) apply. **[S]**

This is a role/membership prerequisite audit. The authoritative standalone
[#487 source audit](project-scoped-remotes-contract-audit.md),
[source design](../implementation/project-remote-sources.md), and its **entire
P11 completion matrix** remain in force. They still require effective reads,
native and virtual paths, warm-cache and in-flight revocation, discovery,
configuration parity and incarnation fencing. The producer sharing-mode wire
mutation remains unknown: neither `readOnly` nor `shared_read_only` specifies that
mode. Completing this prerequisite cannot close #487. **[S]**

## Role visibility, types and mutation

The list definition returns all project roles as a JSON array. `ProjectRole`
contains `name`, `description`, `type`, `environments:string[]` and
`actions:string[]`; its type enum covers `PREDEFINED`, `CUSTOM` and
`CUSTOM_GLOBAL`. The definition identifies those as built-in, project custom and
global custom roles, respectively. It does not prescribe ordering or a
same-name collision policy.
([Role list definition](https://docs.jfrog.com/projects/reference/getprojectroles.md)). **[D]**

The local global Project Admin definition uses `ADMIN`, which is absent from
that project response enum. A project HTTP view therefore needs an explicit
representation decision before reusing global role values unchanged; the enum
does not establish different default actions or override behavior.
([Global model](../../internal/access/core/model/roles.go),
[project response schema](https://docs.jfrog.com/projects/reference/getprojectroles.md)). **[S/D/I]**

The authored API explicitly requires all three categories in the effective
project list, with global custom roles usable in projects. Do not return only
stored project rows. Global availability does not itself assign every platform
user to every project: the concepts guide associates project membership with
assigning at least one global or project role in the selected project.
([Authored project API](../../api/src/access/paths-projects.yaml),
[role concepts](https://docs.jfrog.com/projects/docs/global-and-project-role-types)). **[S/D]**

All paths below start `/access/api/v1/projects/{projectKey}`. The response
descriptions below are schema contracts, not captured runtime bodies.

| Operation | Success and body | Documented errors | Privilege |
| --- | --- | --- | --- |
| `GET /roles` | 200, `ProjectRole[]` | 401/403/404 | Platform Admin, Project Admin, or `project:<key>/roles:r`. [Definition](https://docs.jfrog.com/projects/reference/getprojectroles.md). **[D]** |
| `GET /roles/{role}` | 200, `ProjectRole` | 401/403/404 | Platform Admin or Project Admin. [Definition](https://docs.jfrog.com/projects/reference/getprojectrole.md). **[D]** |
| `POST /roles` | 201, `ProjectRole`; body requires `name` and `type` | 400/401/403/404 | Platform Admin, or Project Admin with Manage Members. [Definition](https://docs.jfrog.com/projects/reference/createprojectrole.md). **[D]** |
| `PUT /roles/{role}` | 200, `ProjectRole`; body requires `type`, permits description/actions/environments | 400/401/403/404 | Same Manage Members requirement. Only `CUSTOM` can be updated. [Definition](https://docs.jfrog.com/projects/reference/updateprojectrole.md). **[D]** |
| `DELETE /roles/{role}` | 204, no response content | 401/403/404 | Same Manage Members requirement. No documented 400 in the status table. [Definition](https://docs.jfrog.com/projects/reference/deleteprojectrole.md). **[D]** |

These Projects definitions use `{"errors":[{"code":"...","message":"..."}]}`
with string code/message, rather than Artifactory's numeric `status` error
entries. No `required` arrays occur in the role response or error schemas.
([Create definition, components](https://docs.jfrog.com/projects/reference/createprojectrole.md)). **[D]**

Global overrides and project mutations are different contracts. The concepts
guide permits global predefined action customization, prohibits their
rename/delete, and permits custom global rename from 7.77. The current global
management guide's broad statement that inherited project roles can be edited
does not identify a project override wire operation; the project PUT definition
specifically permits only `CUSTOM`. Do not infer project `PREDEFINED` or
`CUSTOM_GLOBAL` override support from global editability.
([Role types](https://docs.jfrog.com/projects/docs/global-and-project-role-types),
[global management](https://docs.jfrog.com/projects/docs/manage-global-roles),
[project PUT](https://docs.jfrog.com/projects/reference/updateprojectrole.md)). **[D/I]**

The local global service overlays stored predefined actions/environments on
defaults, keeps built-in descriptions, rejects Project Admin changes, and
prohibits renaming every role. Its custom-global rename restriction is therefore
an existing local policy, not proof of the vendor's current behavior.
([Global service](../../internal/access/core/roles/roles.go),
[ADR0013](decisions/0013-projects-and-environments.md)). **[S]**

## Validation: what is established and what is not

| Concern | Evidence and implementation boundary |
| --- | --- |
| Project role type | Tagged `project_role` accepts only `CUSTOM`; project PUT documents that restriction. The POST schema itself merely requires a string type. Creating a project-custom role as `CUSTOM` is the authored contract. [Provider source](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_role.go), [authored API](../../api/src/access/paths-projects.yaml). **[S/D]** |
| Name and immutability | The provider validates length 1–64 and forces replacement on name change. The REST POST schema has no length/pattern; PUT has no name field. The authored API requires a unique immutable name. These sources do not settle whitespace trimming, character exclusions, Unicode counting, case folding or REST rejection above 64. [Provider](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_role.go), [POST](https://docs.jfrog.com/projects/reference/createprojectrole.md), [authored API](../../api/src/access/paths-projects.yaml). **[S/D/I]** |
| Minimum actions | The management guide says roles cannot be empty. REST arrays have no `minItems`; the tagged provider requires an actions set but installs no size or element validator on it. An exact REST outcome for omitted/empty actions is unproved. Existing global service acceptance of empty sets does not settle project behavior. [Guide](https://docs.jfrog.com/projects/docs/manage-project-roles), [provider](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_role.go), [POST](https://docs.jfrog.com/projects/reference/createprojectrole.md). **[D/S/I]** |
| Number of roles | The guide says at most 30 roles per project. It does not define whether predefined/global roles count, and the REST definition has no limit property. The global guide separately says 30 custom global roles. A CUSTOM-only project limit or exact overflow response would be a choice, not an established REST rule. [Project guide](https://docs.jfrog.com/projects/docs/manage-project-roles), [global guide](https://docs.jfrog.com/projects/docs/manage-global-roles). **[D/I]** |
| Environments | The project guide lets users select global and project environments and calls assignment optional. The provider requires an environment set without a default/size/element validator; REST arrays have no default. `["DEV"]` defaulting, empty meaning all/none, case canonicalization, duplicates and unknown-name statuses are not settled by those sources. [Guide](https://docs.jfrog.com/projects/docs/manage-project-roles), [provider](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_role.go), [PUT](https://docs.jfrog.com/projects/reference/updateprojectrole.md). **[D/S/I]** |

For artifactweave, the authored project API explicitly rejects unknown actions
and environments and duplicate project role names with 400. ADR0013 chooses
case-insensitive environment lookup with stored spelling; it also permits both
global and own-project environments for project resources. Extending that
canonicalization to project role inputs is consistent with the local design,
but is not independently observed vendor role validation. The existing global
service deduplicates actions/environments in input order and resolves only
global environments. **Do not reuse that global-only resolver for project roles.**
([Authored API](../../api/src/access/paths-projects.yaml),
[ADR0013](decisions/0013-projects-and-environments.md),
[global role validation](../../internal/access/core/roles/roles.go)). **[S/I]**

**Unresolved collision case:** the local global service checks global names only;
a `CUSTOM_GLOBAL` added later can have the name of an existing project `CUSTOM`
row. The store keys roles by `(ProjectKey, Name)` and memberships store names
alone. Neither the cited REST definitions, guides nor tagged provider determine
whether the vendor prevents this pair, lists both, shadows one in name lookup,
or resolves membership/token roles by precedence. No global reservation across
all projects, case-insensitive role uniqueness or shadow order is established.
This needs an explicit compatibility decision or first-party fixture before a
service can claim complete collision parity.
([Global create](../../internal/access/core/roles/roles.go),
[store contract](../../internal/access/core/port/projects.go),
[membership model](../../internal/access/core/model/project.go),
[role list definition](https://docs.jfrog.com/projects/reference/getprojectroles.md)). **[S/I]**

## Exact project action vocabulary

The tagged first-party provider lists these **32** project actions; the authored
API adopts them. In the framework provider they are a descriptive list, not an
installed per-element validator. The REST role schemas expose string arrays,
not an action enum. **[S]**
([Provider source](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_role.go),
[authored API](../../api/src/access/paths-projects.yaml)).

```text
READ_REPOSITORY                  ANNOTATE_REPOSITORY
DEPLOY_CACHE_REPOSITORY          DELETE_OVERWRITE_REPOSITORY
MANAGE_XRAY_MD_REPOSITORY        READ_RELEASE_BUNDLE
ANNOTATE_RELEASE_BUNDLE          CREATE_RELEASE_BUNDLE
DISTRIBUTE_RELEASE_BUNDLE        DELETE_RELEASE_BUNDLE
MANAGE_XRAY_MD_RELEASE_BUNDLE    READ_BUILD
ANNOTATE_BUILD                   DEPLOY_BUILD
DELETE_BUILD                    MANAGE_XRAY_MD_BUILD
READ_SOURCES_PIPELINE            TRIGGER_PIPELINE
READ_INTEGRATIONS_PIPELINE       READ_POOLS_PIPELINE
MANAGE_INTEGRATIONS_PIPELINE     MANAGE_SOURCES_PIPELINE
MANAGE_POOLS_PIPELINE            TRIGGER_SECURITY
ISSUES_SECURITY                 LICENCES_SECURITY
REPORTS_SECURITY                WATCHES_SECURITY
POLICIES_SECURITY               RULES_SECURITY
MANAGE_MEMBERS                  MANAGE_RESOURCES
```

`READ_POLICIES_SECURITY` belongs to the local 25-action global vocabulary and is
absent here. The eight project-only additions are the three `MANAGE_*_PIPELINE`
actions, `TRIGGER_SECURITY`, `ISSUES_SECURITY`, `LICENCES_SECURITY`,
`MANAGE_MEMBERS` and `MANAGE_RESOURCES`. Global custom roles inherited into a
project remain global definitions; validating their actions as newly submitted
project-custom actions would incorrectly reject existing global values.
([Global model at the audited base](../../internal/access/core/model/roles.go),
[authored project schemas](../../api/src/access/paths-projects.yaml)). **[S/I]**

The local `RoleActions` maps repository/build actions to artifact/build
permissions. It currently grants nothing for release-bundle/pipeline/security
actions; the authored project schemas explicitly promise round-tripping
pipeline/security values without granting those products' permissions.
`MANAGE_MEMBERS` and `MANAGE_RESOURCES` must not silently become artifact
read/write grants.
([Action mapper](../../internal/access/core/model/roles.go),
[authored API](../../api/src/access/paths-projects.yaml)). **[S]**

## Membership operations and cleanup

User/group and role prerequisites are explicit in the assignment guide.
The tagged provider's embedded project memberships also say users/groups must
already exist. The dedicated user resource treats 404 on create as a missing
user; `ignore_missing_user` suppresses failure while saying no membership was
created. This is client evidence, not a documented error-message guarantee.
([Assignment guide](https://docs.jfrog.com/projects/docs/step-2-assign-members-to-a-project),
[project provider](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project.go),
[user resource](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_user.go)). **[D/S]**

| Operations, under the same project prefix | Published response and policy |
| --- | --- |
| `GET /users`, `GET /groups` | 200, `{"members":[{"name":"...","roles":["..."]}]}`; 401/403/404. Platform Admin, Project Admin with Manage Members, or the matching `project:<key>/members/users:r` / `members/groups:r` scope. [Users](https://docs.jfrog.com/projects/reference/getprojectusers.md), [groups](https://docs.jfrog.com/projects/reference/getprojectgroups.md). **[D]** |
| `GET /users/{user}`, `GET /groups/{group}` | 200, one `{name,roles[]}`; 401/403/404. Platform Admin or Project Admin with Manage Members. [User](https://docs.jfrog.com/projects/reference/getprojectuser.md), [group](https://docs.jfrog.com/projects/reference/getprojectgroup.md). **[D]** |
| `PUT /users/{user}`, `PUT /groups/{group}` | 200, one `{name,roles[]}`; 400/401/403/404. Required JSON body with optional `roles:string[]`, no schema minimum. Same Manage Members policy. The published PUT 404 prose names missing project only. [User PUT](https://docs.jfrog.com/projects/reference/addorupdateprojectuser.md), [group PUT](https://docs.jfrog.com/projects/reference/updateprojectgroup.md). **[D]** |
| `DELETE /users/{user}`, `DELETE /groups/{group}` | 204, no content; 401/403/404. Same Manage Members policy. [User delete](https://docs.jfrog.com/projects/reference/deleteprojectuser.md), [group delete](https://docs.jfrog.com/projects/reference/deleteprojectgroup.md). **[D]** |

Both dedicated membership resources enforce a roles-set minimum of one.
The local API requires every assigned role to exist, treats PUT as replacement,
and requires at least one role. Thus an empty membership is outside the agreed
local contract; exact vendor REST behavior for omitted/empty/null roles remains
unobserved. There is no evidence that empty PUT is an alias for DELETE.
([User validator](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_user.go),
[group validator](https://github.com/jfrog/terraform-provider-project/blob/v1.9.9/pkg/project/resource/resource_project_group.go),
[authored API](../../api/src/access/paths-projects.yaml)). **[S/I]**

The authored API lists direct user roles separately from group inheritance;
removing direct membership leaves group roles. It requires project-role delete
to remove that role from memberships. The existing global service removes the
membership if its last role disappears. Vendor role-delete documentation does
not specify cleanup or last-role behavior; applying the same atomic cleanup to
project roles is a local requirement/choice, not a documented vendor response.
Cleanup must respect the unresolved same-name global/project case above.
([Authored API](../../api/src/access/paths-projects.yaml),
[global cleanup](../../internal/access/core/roles/roles.go),
[vendor delete](https://docs.jfrog.com/projects/reference/deleteprojectrole.md)). **[S/I]**

Project Admin powers depend on privileges delegated by Platform Admin:
Manage Members governs member management, while resource administration has
separate privileges. A custom action string is not evidence that its holder
qualifies as the Project Admin required by these endpoint policies.
([Project Admin guide](https://docs.jfrog.com/projects/docs/project-admin-role),
[project-role POST](https://docs.jfrog.com/projects/reference/createprojectrole.md)). **[D/I]**

## Role tokens and project/resource scopes

`applied-permissions/roles:<project>:<role>[,<role>...]` applies named project role
permissions; multiword names are quoted. Project management scopes instead
provide read access to members/roles, including the documented
`project:<key>/members/**:r` wildcard. They are not artifact role assignments.
The resource-scope guide permits standalone resource access without predefined
platform/project permissions, but does not specify mixed role/resource
intersection or union semantics.
([Scoped-token guide](https://docs.jfrog.com/administration/reference/create-scoped-token)). **[D/I]**

Project-admin-minted tokens are tied to projects where the issuer is admin and
have the named roles' permissions **at use time**, across relevant builds,
repositories and artifacts. Named roles are not an immutable action snapshot.
The guide does not explicitly say an existing token subject must already hold
every named role through user/group membership.
([Access-token guide](https://docs.jfrog.com/administration/docs/access-tokens#access-token-creation-by-project-admins)). **[D/I]**

The official project-mapping REST example omits `username`, selects Developer in
`token_spec.scope`, and states the exchanged token carries the mapping's
permissions. It requires role definitions in the target project and derives the
mapping's single project key from scope. The tagged platform provider likewise
shows a username-less Project Admin/Developer mapping, although its generated
field text contradictorily calls username required. Prefer the official REST
example over that contradictory provider prose.
([Project mapping guide](https://docs.jfrog.com/administration/reference/manage-project-scoped-identity-mappings),
[tagged mapping example](https://github.com/jfrog/terraform-provider-platform/blob/v2.2.11/docs/resources/oidc_identity_mapping.md)). **[D/S]**

**Consequence [I]:** authorization needs a role-scope path that can resolve named
roles without a stored-user membership row; otherwise that documented CI flow
cannot work. This is an inference from the published flow, not a captured claim
about every existing-user token. Ordinary membership existence validation must
not be used to reject username-less token identities. Whether a token naming a
role unassigned to an existing subject is independently granted that role, and
the exact interaction with additional user/group scopes, still needs explicit
vendor confirmation or an acknowledged compatibility decision.
([Project mapping example](https://docs.jfrog.com/administration/reference/manage-project-scoped-identity-mappings),
[scoped roles](https://docs.jfrog.com/administration/reference/create-scoped-token)).

At the audited base, `kernel.Scope` already parses project roles and project
management entries, including quoted role names. Token HTTP mint validation
permits non-user scopes only to platform admins, so documented Project Admin
minting remains absent. `authz.analyse` marks role scopes as applied permissions
but deliberately grants nothing for them. This placeholder cannot establish
membership intersection rules. Artifact scopes currently narrow existing rights
locally, tracked as DV-092/#476 against the vendor's standalone resource grants.
([Scope parser](../../internal/kernel/scope.go),
[mint validation](../../internal/access/adapters/http/tokens.go),
[authorization placeholder](../../internal/access/core/authz/authz.go),
[ADR0006](decisions/0006-authorization-native-permission-model.md)). **[S]**

For #487, the repository requires a current receiving-project identity and
intersection of caller policy, token restriction and share ceiling before a
response is admitted. A project management read scope cannot authorize artifact
retrieval or cross-project discovery by itself. Mixed scopes must not erase a
resource restriction or widen the source permit. These are local #487 safety
requirements, not a claim that public vendor docs settle every mixed-scope edge.
([P11 authorization design](../implementation/project-remote-sources.md#p11-configuration-and-authorization),
[full completion matrix](../implementation/project-remote-sources.md#p11-completion-matrix-for-487)). **[S/I]**

## Precise remaining gaps

During this audit the prerequisite work added a separate project-action
vocabulary and private `project.Service` role/membership operations, followed by
separate current-assignment and named-definition lookups. At that prerequisite
snapshot they activated no project HTTP/authz workflow. The foundation preserves omitted/empty action and
environment sets, adds no inferred 30-role REST limit, and refuses ambiguous
global/project names instead of selecting a shadow winner. Those are explicit
local development choices while the gaps below remain open; they are not a
claim of end-to-end project administration or vendor collision parity.
([Project model](../../internal/access/core/model/project_roles.go),
[private service](../../internal/access/core/project/roles.go),
[membership operations](../../internal/access/core/project/membership.go),
[definition lookups](../../internal/access/core/project/assignments.go)). **[S/I]**

The lookups ignore currently deleted identities and resolve definitions at use
time. Identity deletion also clears direct project assignments inside the same
transaction, guarded before identity rows; same-name recreation, real-backend
rollback and concurrent assignment tests exercise that local lifecycle invariant.
It is not a documented vendor cascade. Effective content and role-token
authorization remain required before enabling the full workflow.
([Identity lifecycle audit](identity-deletion-project-grants-audit.md),
[cleanup](../../internal/access/core/project/principals.go)). **[S/I]**

P11 PRJ-01 now binds the documented administration routes through explicit
[HTTP conversions](../../internal/access/adapters/http/project_wire.go) and
[current transactional management checks](../../internal/access/adapters/http/project_access.go).
The Project Admin storage type `ADMIN` projects to the authored project schema's
`PREDEFINED`; its protected internal identity does not change. Lifecycle and
environment-reference changes share the access/artifacts transaction, with
[real SQL rollback tests](../../internal/access/adapters/sql/project_lifecycle_test.go).
[Production-router cases](../../test/server/access/projects_status_test.go) cover
own/foreign/group admins, ordinary members and revoked Manage Members delegation.
These are local implementation facts. They do not settle the vendor inferences
below, activate effective content grants (PRJ-02), or complete #487. **[S/I]**

The bounded sources above do not settle role-name case/normalization,
cross-category collision precedence/reservation, REST role-name length,
action/environment omission and empty defaults, per-project role-limit counting,
partial PUT retention, delete cleanup/final-member behavior, unknown-role and
unknown-principal exact error statuses/messages, or mixed token-scope algebra.
Those edges must remain **[I]** even when the authored API chooses behavior.
The 32-action vocabulary, CRUD payloads/statuses, all three listing categories,
member existence/client minimums, and documented endpoint privilege boundaries
can be implemented without inventing those missing contracts. None supplies the
missing producer sharing-mode API or replaces #487's full acceptance matrix.
